// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package integration runs the generated servers, feeds and store adapters
// over protoc-gen-store's generated repositories on a real PostgreSQL.
//
// Set SERVICE_TEST_DSN to a database the tests may drop and recreate the acme
// schemas in, e.g.
//
//	SERVICE_TEST_DSN='host=localhost port=55437 user=postgres dbname=svc sslmode=disable' go test ./integration/...
//
// Without it every test is skipped.
package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/the-protobuf-project/service/examples/generated/gorm/acme"
	"github.com/the-protobuf-project/service/examples/generated/repository/repox"
)

// open connects to SERVICE_TEST_DSN with every acme schema dropped and
// migrated afresh, so each test starts from empty tables.
func open(t *testing.T) repox.Conn {
	t.Helper()
	dsn := os.Getenv("SERVICE_TEST_DSN")
	if dsn == "" {
		t.Skip("SERVICE_TEST_DSN is not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"booking", "calendar", "directory", "requestkeys", "webhook"} {
		if err := db.Exec(`DROP SCHEMA IF EXISTS "` + s + `" CASCADE`).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := acme.Default.EnsureSchemas(db); err != nil {
		t.Fatal(err)
	}
	if err := acme.Default.Migrate(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sql, err := db.DB(); err == nil {
			_ = sql.Close()
		}
	})
	return repox.Conn{Gorm: db}
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

// wantStatus fails unless err is a status of code with ErrorInfo reason.
func wantStatus(t *testing.T, err error, code codes.Code, reason string) *status.Status {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok || err == nil {
		t.Fatalf("want %s %s, got %v", code, reason, err)
	}
	if st.Code() != code {
		t.Fatalf("want code %s, got %s: %s", code, st.Code(), st.Message())
	}
	if got := reasonOf(st); got != reason {
		t.Fatalf("want reason %s, got %q: %s", reason, got, st.Message())
	}
	return st
}

func reasonOf(st *status.Status) string {
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	return ""
}

func domainOf(st *status.Status) string {
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetDomain()
		}
	}
	return ""
}

// must is v, panicking on err: a test that cannot take its next step has
// failed, and the panic names where.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func statusOf(err error) (*status.Status, bool) { return status.FromError(err) }
