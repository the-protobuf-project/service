// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package rustgen

// cdn.go adds rust_cdn=true to the Rust application shell: [cdn] in the
// configuration and app::open_cdn, the Cloudflare CDN purger over runtime-rs's
// cache::cdn. Go's shell always has it (Connection.CDN); Rust's is opt-in
// because tpp-cache's cdn feature links a network client a service that does
// not purge has no use for.
//
// The purger is the application's to open: its handlers and hooks are built
// before app::run, so they open it from the loaded configuration and keep it.

import "strings"

// appCDN adds app::open_cdn to the shell's module.
func appCDN(module string) string {
	return module + `
/// Opens the Cloudflare CDN purger [cdn] configures, or None when it is off:
/// cache tags, URLs, hosts or prefixes of zone cdn.zone_id, purged with
/// cdn.api_token. Purge after a write commits, so the next visitor's request
/// reaches the origin and sees it. Handlers and hooks are built before run,
/// so open it from app::config::load()'s configuration and hand it to them.
pub async fn open_cdn(
    cfg: &config::CdnConfig,
) -> Result<Option<Arc<::cache::cdn::Purger>>, ::cache::CacheError> {
    if !cfg.enabled {
        return Ok(None);
    }
    let purger = ::cache::cdn::Purger::connect(::cache::cdn::PurgeConfig {
        zone_id: cfg.zone_id.clone(),
        api_token: cfg.api_token.clone(),
        ..Default::default()
    })
    .await?;
    Ok(Some(Arc::new(purger)))
}
`
}

// cdnConfig adds [cdn] to the configuration.
func cdnConfig(name, config, release string) (string, string) {
	section := "    /// The Cloudflare CDN purger (rust_cdn=true), off unless enabled.\n    #[serde(default)]\n    pub cdn: CdnConfig,\n"
	types := `
/// Purges what Cloudflare's CDN has cached for one zone (rust_cdn=true).
#[derive(Debug, Clone, Default, Deserialize)]
pub struct CdnConfig {
    /// Opens the purger. Off, app::open_cdn returns None.
    #[serde(default)]
    pub enabled: bool,
    /// The zone purged.
    #[serde(default)]
    pub zone_id: String,
    /// Needs Cache Purge on the zone. A secret.
    #[serde(default)]
    pub api_token: String,
}
`
	validate := "        if self.cdn.enabled && (self.cdn.zone_id.is_empty() || self.cdn.api_token.is_empty()) {\n            return Err(\"cdn.enabled requires cdn.zone_id and cdn.api_token\".into());\n        }\n"
	config = strings.Replace(config, "    /// The hybrid server.\n    pub server: ServerConfig,\n", "    /// The hybrid server.\n    pub server: ServerConfig,\n"+section, 1)
	config = strings.Replace(config, "\n/// Service identity.\n#[derive", types+"\n/// Service identity.\n#[derive", 1)
	config = strings.Replace(config, "        let srv = &self.server;\n        if self.app.name.is_empty()", validate+"        let srv = &self.server;\n        if self.app.name.is_empty()", 1)
	release += "\n# Cloudflare CDN purge (rust_cdn=true). Secret: set " + envPrefix(name) + "CDN__API_TOKEN at deploy time.\n[cdn]\nenabled = false\nzone_id = \"\"\napi_token = \"\"\n"
	return config, release
}
