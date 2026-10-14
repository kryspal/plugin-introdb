# plugin-introdb

Looks up intro, recap and outro times from the IntroDB community database. Lookups need no key; uploading your own manual markings is off by default. An official [Kryspal Media](https://github.com/kryspal) plugin, built on [plugin-sdk](https://github.com/kryspal/plugin-sdk).

```sh
go build -o introdb .
go vet ./... && go test ./...
```

Implementation notes: [docs/design.md](docs/design.md).

Licensed under the Apache License 2.0 (see `LICENSE` and `NOTICE`).

Official builds are signed and published through the Kryspal plugin catalog; builds from source are treated as third-party plugins.
