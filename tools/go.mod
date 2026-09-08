// Pinned dev/security tooling for the Makefile gates.
//
// These live in their own module, not in the root go.mod, on purpose. A tool
// directive puts the tool's whole transitive dependency graph into the module
// that declares it, and this repository IS a library imported by other
// uni-chat engine adapters — so a tool directive at the root would drag
// staticcheck's and govulncheck's own dependency trees into every consumer's
// module graph, and into what `make dependency-check` scans as this module's
// dependency state. Before this split, the root go.mod carried
// golang.org/x/mod@0.35.0 as a transitive dependency of the pinned
// honnef.co/go/tools@v0.8.1 toolchain — vulnerable to GO-2026-6179/GO-2026-6180
// (CVE-2026-56865/CVE-2026-56864) — and made every OSV-Scanner run against the
// library red for a CVE the library itself neither imports nor is affected by.
//
// Isolating them here keeps every guarantee 05-build-test-docs.md asks for:
// each tool is pinned by exact version in a committed go.mod/go.sum, resolved
// from that manifest and never from PATH, and built and run by the same Go
// toolchain as the project (`go -C tools tool <name>`), which is the export-data
// compatibility the rule exists to protect. Nothing here is linked into, or
// scanned as part of, the library consumers actually import.
module github.com/MikcleGrok/uni-chat-sdk/tools

go 1.26.6

tool (
	golang.org/x/vuln/cmd/govulncheck
	honnef.co/go/tools/cmd/staticcheck
)

require (
	github.com/BurntSushi/toml v1.4.1-0.20240526193622-a339e1f7089c // indirect
	golang.org/x/exp/typeparams v0.0.0-20231108232855-2478ac86f678 // indirect
	golang.org/x/mod v0.39.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/telemetry v0.0.0-20260811182544-a038080d80e5 // indirect
	golang.org/x/tools v0.49.0 // indirect
	golang.org/x/vuln v1.7.0 // indirect
	honnef.co/go/tools v0.8.1 // indirect
)
