// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package rustgen

// store.go adds store=true (and cache=true) to the Rust application shell:
// the records database opened from [database], every package's
// repositories built over it by protoc-gen-store's lang=rust output, wrapped
// by protoc-gen-cache's cache_repositories (the cache module protoc-gen-cache
// writes beside each package's repositories) when [cache] is enabled, and
// every standard method's handler filled from them.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/the-protobuf-project/service/plugin/generator"
	"github.com/the-protobuf-project/service/plugin/spec"
)

// sqlxPool is the sqlx pool options type of each rust_database dialect.
var sqlxPool = map[string]string{
	"postgres": "postgres::PgPoolOptions",
	"mysql":    "mysql::MySqlPoolOptions",
	"sqlite":   "sqlite::SqlitePoolOptions",
}

// storePackage is one proto package whose repositories the shell builds.
type storePackage struct {
	field  string // app::Repositories field, "acme_directory_v1"
	path   string // the package's module under a root, "::acme::directory::v1"
	cached bool   // a resource of it carries (cache.v1.cache)
}

// storePackages are the packages of the resources the services serve.
func storePackages(services []appService) ([]storePackage, map[*spec.Resource]string) {
	byPkg := map[string]*storePackage{}
	fieldOf := map[*spec.Resource]string{}
	for _, s := range services {
		for _, r := range s.svc.Resources {
			pkg := string(r.Message.Desc.ParentFile().Package())
			sp := byPkg[pkg]
			if sp == nil {
				var parts []string
				var path string
				for _, part := range strings.Split(pkg, ".") {
					parts = append(parts, strings.TrimPrefix(ident(part), "r#"))
					path += "::" + ident(part)
				}
				sp = &storePackage{field: ident(strings.Join(parts, "_")), path: path}
				byPkg[pkg] = sp
			}
			sp.cached = sp.cached || generator.CacheOf(r.Message).Any()
			fieldOf[r] = sp.field
		}
	}
	var out []storePackage
	for _, sp := range byPkg {
		out = append(out, *sp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].field < out[j].field })
	return out, fieldOf
}

// appStore adds the repositories, their wiring and the opened database to
// the shell's module.
func (t *Target) appStore(module string, services []appService) string {
	pkgs, fieldOf := storePackages(services)
	var b strings.Builder
	b.WriteString("\n/// Every package's repositories (store=true), as protoc-gen-store's lang=rust\n/// repository output builds them: what the standard methods persist through.\npub struct Repositories {\n")
	for _, p := range pkgs {
		fmt.Fprintf(&b, "/// %s.\npub %s: %s%s::Repositories,\n", strings.TrimPrefix(p.path, "::"), p.field, t.Settings.RepositoryModule, p.path)
	}
	b.WriteString("}\n\n/// Builds every package's repositories over db, the descriptors in pool\n/// describing their records.\npub fn repositories(\ndb: &::database::records::DB,\npool: &prost_reflect::DescriptorPool,\n) -> ::database::Result<Repositories> {\nOk(Repositories {\n")
	for _, p := range pkgs {
		fmt.Fprintf(&b, "%s: %s%s::repositories(db, pool)?,\n", p.field, t.Settings.RepositoryModule, p.path)
	}
	b.WriteString("})\n}\n\nimpl Handlers {\n/// Answers every standard method of every service from repos; the custom\n/// methods stay the application's to register.\npub fn with_repositories(mut self, repos: &Repositories) -> Self {\n")
	for _, s := range services {
		if !s.svc.Standard() {
			continue
		}
		fmt.Fprintf(&b, "self.%s = self.%s.with_repositories(&%s::%sRepositories {\n", s.field, s.field, s.path, s.svc.GoName)
		for _, r := range s.svc.Resources {
			fmt.Fprintf(&b, "%s: repos.%s.%s.clone(),\n", repoField(r), fieldOf[r], repoField(r))
		}
		b.WriteString("});\n")
	}
	b.WriteString("self\n}\n}\n")
	fmt.Fprintf(&b, `
/// Opens the records database [database] configures: a %[1]s pool of
/// database.max_connections (0 keeps sqlx's default) over database.url, the
/// records driver over it.
pub async fn open_database(cfg: &config::DatabaseConfig) -> Result<::database::records::DB, Error> {
    let mut options = ::database::orm::sqlx::%[2]s::new();
    if cfg.max_connections > 0 {
        options = options.max_connections(cfg.max_connections);
    }
    let pool = options
        .connect(&cfg.url)
        .await
        .map_err(|e| Error::Open(format!("database: {e}")))?;
    Ok(::database::orm::driver(pool))
}
`, t.Settings.Database, sqlxPool[t.Settings.Database])
	if t.Settings.Cache {
		b.WriteString(`
/// Opens the cache [cache] configures, or None when it is off: a Redis (or
/// Dragonfly) client at cache.address, keys under cache.prefix, the
/// cache.database namespace selected.
pub async fn open_cache(cfg: &config::CacheConfig) -> Result<Option<Arc<::cache::DB>>, Error> {
    use ::cache::Provider as _;
    if !cfg.enabled {
        return Ok(None);
    }
    let client = ::cache::drivers::redis::RedisClient::connect(::cache::drivers::redis::RedisConfig {
        address: cfg.address.clone(),
        ..Default::default()
    })
    .await
    .map_err(|e| Error::Open(format!("cache: {e}")))?;
    let provider = ::cache::drivers::redis::RedisProvider::new(
        Arc::new(client),
        ::cache::Config {
            prefix: cfg.prefix.clone(),
            ..Default::default()
        },
    );
    let db = provider
        .set_database(&cfg.database)
        .await
        .map_err(|e| Error::Open(format!("cache: {e}")))?;
    Ok(Some(Arc::new(db)))
}

/// Wraps every package's repositories whose resources carry cache.v1
/// annotations with protoc-gen-cache's cache_repositories over cache: the
/// package's cache module beside its repositories.
pub fn cache_repositories(repos: Repositories, cache: Arc<::cache::DB>) -> Repositories {
    #[allow(unused_mut)]
    let mut repos = repos;
`)
		for _, p := range pkgs {
			if p.cached {
				fmt.Fprintf(&b, "repos.%s = %s%s::cache::cache_repositories(repos.%s, cache.clone());\n", p.field, t.Settings.RepositoryModule, p.path, p.field)
			}
		}
		b.WriteString("drop(cache);\nrepos\n}\n")
	}
	module += b.String()

	// The error variants and run's opening of the database.
	module = strings.Replace(module, "    /// The hybrid server failed.\n    Server(grpc::Error),\n}", "    /// The hybrid server failed.\n    Server(grpc::Error),\n    /// The database or cache did not open.\n    Open(String),\n    /// The repositories could not be built.\n    Database(::database::Error),\n}", 1)
	module = strings.Replace(module, "            Error::Server(e) => write!(f, \"server: {e}\"),\n", "            Error::Server(e) => write!(f, \"server: {e}\"),\n            Error::Open(e) => write!(f, \"open: {e}\"),\n            Error::Database(e) => write!(f, \"repositories: {e}\"),\n", 1)
	open := `    let db = open_database(&cfg.database).await?;
    #[allow(unused_mut)]
    let mut repos = repositories(&db, &pool).map_err(Error::Database)?;
`
	if t.Settings.Cache {
		open += `    if let Some(cache) = open_cache(&cfg.cache).await? {
        repos = cache_repositories(repos, cache);
    }
`
	}
	open += `    let handlers = provision.handlers.with_repositories(&repos);
    let servers = assemble(servicex::Options::new(pool), handlers, provision.hooks)`
	module = strings.Replace(module, "    let servers = assemble(servicex::Options::new(pool), provision.handlers, provision.hooks)", open, 1)
	module = strings.Replace(module, "/// Runs the application to completion: loads the configuration, assembles\n/// every server from the provision,", "/// Runs the application to completion: loads the configuration, opens the\n/// database (and the cache) and builds the repositories, assembles every\n/// server from the provision with its standard methods answered by them,", 1)
	return module
}

// storeConfig adds [database] (and [cache]) to the configuration.
func (t *Target) storeConfig(name, config, release, dev string) (string, string, string) {
	sections := "    /// The records database the repositories persist in.\n    pub database: DatabaseConfig,\n"
	types := `
/// The records database (store=true).
#[derive(Debug, Clone, Deserialize)]
pub struct DatabaseConfig {
    /// The connection URL, e.g. postgres://user:password@host:5432/db.
    pub url: String,
    /// The pool's size; 0 keeps sqlx's default.
    #[serde(default)]
    pub max_connections: u32,
}
`
	validate := "        if self.database.url.is_empty() {\n            return Err(\"database.url is required\".into());\n        }\n"
	releaseTOML := "\n[database]\n# Secret: set " + envPrefix(name) + "DATABASE__URL at deploy time.\nurl = \"\"\nmax_connections = 10\n"
	devURL := map[string]string{
		"postgres": "postgres://postgres:postgres@localhost:5432/" + name,
		"mysql":    "mysql://root:root@localhost:3306/" + name,
		"sqlite":   "sqlite://" + name + ".db?mode=rwc",
	}[t.Settings.Database]
	devTOML := "\n[database]\nurl = \"" + devURL + "\"\n"
	if t.Settings.Cache {
		sections += "    /// The cache the repositories read through (cache=true).\n    #[serde(default)]\n    pub cache: CacheConfig,\n"
		types += `
/// The cache (cache=true): Redis or Dragonfly, off unless enabled.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct CacheConfig {
    /// Turns the cache on.
    #[serde(default)]
    pub enabled: bool,
    /// host:port of the server.
    #[serde(default)]
    pub address: String,
    /// The deployment's key prefix.
    #[serde(default)]
    pub prefix: String,
    /// The cache database (key namespace) selected.
    #[serde(default)]
    pub database: String,
}
`
		validate += "        if self.cache.enabled && self.cache.address.is_empty() {\n            return Err(\"cache.enabled requires cache.address\".into());\n        }\n"
		releaseTOML += "\n[cache]\nenabled = false\naddress = \"localhost:6379\"\nprefix = \"" + name + "\"\ndatabase = \"\"\n"
	}
	config = strings.Replace(config, "    /// The hybrid server.\n    pub server: ServerConfig,\n}\n", "    /// The hybrid server.\n    pub server: ServerConfig,\n"+sections+"}\n"+types, 1)
	config = strings.Replace(config, "        let srv = &self.server;\n        if self.app.name.is_empty()", validate+"        let srv = &self.server;\n        if self.app.name.is_empty()", 1)
	return config, release + releaseTOML, dev + devTOML
}
