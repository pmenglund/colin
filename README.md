# Colin

Colin is a Go implementation of the [openai/symphony](https://github.com/openai/symphony)
project. Symphony defines a service that watches an issue tracker, creates an isolated workspace
for each issue, and runs a coding agent against that work. The upstream `openai/symphony`
repository publishes the overall project framing, the service specification, and a reference
implementation in another language. Colin follows that model in this repository's Go codebase.

## Relationship to `openai/symphony`

Colin is not a fork of the upstream implementation. Instead, it is a Go implementation of the same
service concept and specification. If you want the original project overview, the authoritative
public repository is:

- <https://github.com/openai/symphony>

That upstream repository currently includes:

- the project-level README that explains what Symphony is
- the upstream `SPEC.md`
- an experimental reference implementation outside Go

This repository exists to implement that service in Go while keeping the behavior easy to inspect,
test, and evolve in a Go-native codebase.

## How `SPEC.md` Is Used Here

[`SPEC.md`](./SPEC.md) is the working specification for Colin.

Use it as the primary contract for the system's expected behavior:

- when implementing a feature, use `SPEC.md` to understand the intended behavior and boundaries
- when reviewing the code, compare the implementation against `SPEC.md`
- when Colin intentionally diverges from or clarifies the upstream Symphony behavior, update
  `SPEC.md` so the repository documents that decision explicitly

In practice, the Go code in this repository is the implementation, and `SPEC.md` is the document
that explains what that implementation is trying to satisfy.

## Development

Run the test suite with:

```sh
go test ./...
```
