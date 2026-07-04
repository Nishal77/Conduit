// This file exists solely to mark dashboard/ as a separate Go module
// boundary, so `go build ./...` and `go test ./...` from the repo root
// don't descend into dashboard/node_modules — some npm packages (e.g.
// flatted) ship an incidental .go file with no go.mod of their own, which
// Go's tooling would otherwise treat as part of the main conduit module.
// dashboard/ itself contains no Go code; see package.json for its actual
// module manifest.
module github.com/conduit-oss/conduit/dashboard

go 1.23
