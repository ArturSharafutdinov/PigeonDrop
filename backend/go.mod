module github.com/ArturSharafutdinov/PigeonDrop/backend

go 1.26.0

toolchain go1.26.10

require (
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.13.0
	github.com/oapi-codegen/runtime v1.9.0
	github.com/pressly/goose/v3 v3.30.1
	github.com/tphakala/go-opus v1.1.0 // the last release that builds with Go 1.26: v1.2.0 and later need Go 1.27
	golang.org/x/crypto v0.59.0
	golang.org/x/term v0.48.0
	golang.org/x/text v0.44.0
)

require (
	github.com/apapsch/go-jsonmerge/v2 v2.0.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/mfridman/interpolate v0.2.0 // indirect
	github.com/sethvargo/go-retry v0.5.0 // indirect
	github.com/tphakala/simd v1.8.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
)
