{{- /*
Template for go-licenses report.
Produces a Markdown file listing every dependency, its license, and a link
to the upstream license text. Consumed by the release pipeline; the resulting
THIRD_PARTY_NOTICES.md is uploaded as a release asset alongside the binaries.
*/ -}}
# Third-Party Notices

This file lists the third-party Go modules statically linked into the `fir`
binary, together with their licenses. It is generated at release time from
`go.mod` / `go.sum` via `go-licenses`.

`fir` itself is distributed under the MIT License (see `LICENSE`). The
notices below are reproduced to satisfy the attribution requirements of the
upstream licenses.

| Module | License | Source |
|---|---|---|
{{ range . -}}
| `{{ .Name }}` | {{ .LicenseName }} | [{{ .LicenseURL }}]({{ .LicenseURL }}) |
{{ end }}
