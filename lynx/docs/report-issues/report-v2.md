
But there are still three major issues.

### P0 — Symbol URI is not stable enough

Current format:

```
method:api/http/router.go:login
```

I would change it to:

```
method:api/http:router.login
```

or:

```
method:api/http:Router.Login
```

because:

`router.go`

is not an identity.

Files can be renamed.

Symbols cannot.

For example:

```go
func (r *router) login(...)
```

should become:

```
method:api/http:router.login
```

### P1 — Exact Match Boost is working

But Intent Match is still weak.

Query:

```bash
lx search "authentication flow"
```

still returns:

```
authService
AuthServiceOpts
ChangeUserPasswordDto
authConfig
```

instead of:

```
Login
Authenticate
VerifyAccessToken
RegisterAuthRoutes
AuthService
```

This indicates that:

**Exact Symbol Search**

is already good.

But:

**Concept Search**

is not.

Lynx needs to distinguish between:

#### Query Type A

```text
login
```

→ symbol lookup

and

#### Query Type B

```text
authentication flow
```

→ intent discovery

These should use two different ranking pipelines.

### P2 — Mock/Test files still appear in results

You already reduced their confidence scores to:

```
8%
12%
13%
```

which is a great improvement.

However, I would go even further.

By default:

```text
mock/
test/
vendor/
generated/
```

should not appear in search results.

They should only be included when explicitly requested:

```bash
lx search "login" --include-tests
```

Similar to how:

`ripgrep`

ignores the following by default:

```text
.git
node_modules
```
