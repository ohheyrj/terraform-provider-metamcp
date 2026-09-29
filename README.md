# Terraform Provider: MetaMCP

Manages [MetaMCP](https://github.com/metatool-ai/metamcp) — namespaces, MCP
servers, endpoints and API keys — as Terraform resources.

## Why this provider talks to an undocumented API

MetaMCP publishes no admin API. Its web UI is a Next.js app backed by tRPC, and
that tRPC surface (`/trpc/frontend/*`) *is* the admin API: the UI has to use it,
so it is stable in practice and comprehensive. This provider speaks that
protocol directly.

Because the API is undocumented, the schemas here were derived from MetaMCP's
own source (`packages/zod-types`) rather than from guesswork, and the wire
format is pinned by unit tests.

## Authentication

Authentication is an email address and password, exchanged for a better-auth
session cookie.

```hcl
provider "metamcp" {
  endpoint = "https://metamcp.example.com"
  email    = "admin@example.com"
  password = var.metamcp_password
}
```

Or via environment variables, which keeps credentials out of HCL and state:

```sh
export METAMCP_ENDPOINT="https://metamcp.example.com"
export METAMCP_EMAIL="admin@example.com"
export METAMCP_PASSWORD="..."
```

### Two things worth knowing

**MetaMCP signs in by email, not by username.** better-auth is configured
without its username plugin and the `users` table has no username column, so a
bare username cannot authenticate.

**An MCP API key cannot be used here.** MetaMCP has two independent auth
systems, and they are not interchangeable:

- **Admin API** (`/trpc/frontend/*`) — better-auth session cookie only. Its
  context is built solely from the `cookie` header; an `Authorization` header is
  never read, so a Bearer token is invisible rather than rejected.
- **MCP gateway** (`/metamcp/<endpoint>/mcp`) — API key or OAuth.

So a key created by `metamcp_api_key` authenticates *MCP clients*; Terraform
itself always needs the email and password (or a session cookie).

### Prefer `cookie` over `password` where you can

```hcl
provider "metamcp" {
  endpoint = "https://metamcp.example.com"
  cookie   = var.metamcp_session_cookie
}
```

A session cookie is a 7-day sliding credential that you can revoke by signing
out. A password is a long-lived credential that is valid until changed. Copy the
cookie from the MetaMCP web UI under DevTools → Application → Cookies →
`better-auth.session_token`.

## How the resources relate

MetaMCP's model is four objects, and the dependency direction is worth stating
because it is not obvious from the names:

- **`metamcp_mcp_server`** — one MCP server: a local process (`STDIO`) or a
  remote service (`SSE`, `STREAMABLE_HTTP`).
- **`metamcp_namespace`** — a group of servers. **A server belongs to a
  namespace through the namespace's `mcp_server_uuids`**, not the other way
  round. That association is authoritative: removing a UUID detaches the server.
- **`metamcp_endpoint`** — the URL clients connect to. It publishes *one*
  namespace, and derives its URL from its own name.
- **`metamcp_api_key`** — a credential for MCP clients. It is scoped to a user,
  **not to an endpoint**: the API has no endpoint field on a key, so one key is
  not limited to one endpoint.

So the shape is: servers → collected into a namespace → published by an
endpoint → reached with an API key.

### Visibility

Servers and namespaces are each either **private** (the default: usable by their
owner alone) or **public** (usable by every user). Both take an `is_public`
attribute, and both default to private when it is left unset.

MetaMCP encodes this as *ownership*: there is no visibility column, and a null
`user_id` is what makes an object public. So `is_public = true` clears ownership
and `is_public = false` claims the object for the authenticated user.

One rule links the two: **a public namespace may only contain public servers.**
Attaching a private server to a public namespace is refused by the API, and the
refusal surfaces with the server's own message. The practical consequence is
that a server bound for a public namespace must itself be public.

## Resources and data sources

| Resource | Purpose |
|---|---|
| `metamcp_namespace` | A group of MCP servers published together |
| `metamcp_mcp_server` | A `STDIO`, `SSE` or `STREAMABLE_HTTP` MCP server |
| `metamcp_endpoint` | The URL MCP clients connect to, publishing one namespace |
| `metamcp_api_key` | A credential for MCP clients |

Each has a matching data source for looking up existing objects by name or UUID.
All four resources support `terraform import`, using the object's UUID as the
import ID; each has a per-resource page under [`docs/`](docs) with an example
and the exact import command.

## Example

```hcl
resource "metamcp_mcp_server" "github" {
  name = "github"
  type = "STREAMABLE_HTTP"
  url  = "https://api.githubcopilot.com/mcp/"

  # Public, so it may be attached to the public namespace below.
  is_public = true
}

resource "metamcp_namespace" "tools" {
  name             = "tools"
  description      = "Shared internal tooling"
  is_public        = true
  mcp_server_uuids = [metamcp_mcp_server.github.uuid]
}

resource "metamcp_endpoint" "tools" {
  name           = "tools"
  namespace_uuid = metamcp_namespace.tools.uuid
}

resource "metamcp_api_key" "ci" {
  name = "ci-pipeline"
}

output "mcp_url" {
  value = metamcp_endpoint.tools.url
}
```

## Security notes

- **`metamcp_api_key.key` is a live credential** and is stored in Terraform
  state. Use encrypted remote state. The attribute is marked sensitive, which
  hides it from CLI output but does *not* encrypt state.
- **`env`, `headers` and `bearer_token` on `metamcp_mcp_server`** are marked
  sensitive for the same reason: these routinely carry upstream API keys.
- **The admin API has no role model.** Any authenticated user can call every
  procedure, including destructive ones. Treat the credentials you give this
  provider as full-admin.
- Read the [SECURITY.md](SECURITY.md) note before reporting anything.

## Development

Tooling comes from [mise](https://mise.jdx.dev) and git hooks from
[hk](https://hk.jdx.dev):

```sh
mise install          # go, golangci-lint, terraform, tfplugindocs, gitleaks, hk
hk check --all        # lint and vet the repository
go test ./...         # all tests, no live instance required
```

`go test ./...` runs the complete suite with no credentials and no network. It
includes resource and data-source tests through the real Terraform plugin
protocol, which run against an in-process fake MetaMCP that reproduces the
envelope, the API's asymmetries and its error shapes.

To run those same tests against a **live** MetaMCP instead — which creates and
destroys real objects — opt in explicitly:

```sh
export METAMCP_LIVE_ACC=1
export METAMCP_ENDPOINT="https://metamcp.example.com"
export METAMCP_EMAIL="admin@example.com"
export METAMCP_PASSWORD="..."
go test ./internal/provider/... -v
```

Live mode is opt-in so that a real instance can never be touched by accident.
Only a live run can confirm that the field names this provider sends match what
the real server accepts; the fake encodes the same beliefs, so it cannot falsify
them.

Regenerate the documentation after changing a schema:

```sh
mise exec -- tfplugindocs generate --provider-name metamcp
```

`docs/` is committed and CI fails if it drifts from the schemas, so run the
command above before opening a pull request.
