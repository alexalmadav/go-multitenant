# Releasing

The repository holds three Go modules: the core (`.`), the Gin adapter
(`middleware/gin`) and the examples (`examples`, never tagged). Each nested
module `require`s the core at a published version and `replace`s it with the
local checkout for development; consumers ignore the replace.

Release vX.Y.Z:

1. `master` is green (unit, integration, PgBouncer jobs).
2. Tag the core: `git tag vX.Y.Z && git push origin vX.Y.Z`.
3. Point the nested modules at it:
   - `middleware/gin/go.mod`: `github.com/alexalmadav/go-multitenant vX.Y.Z`
   - `examples/go.mod`: both requires to `vX.Y.Z`
   - `cd middleware/gin && go mod tidy && cd ../../examples && go mod tidy`
   - commit: `chore: bump nested modules to vX.Y.Z`
4. Tag the adapter: `git tag middleware/gin/vX.Y.Z && git push origin middleware/gin/vX.Y.Z`.
5. `gh release create vX.Y.Z --notes-file ...` listing changes for both modules.
6. Warm the proxy:
   `curl https://proxy.golang.org/github.com/alexalmadav/go-multitenant/@v/vX.Y.Z.info`
   `curl https://proxy.golang.org/github.com/alexalmadav/go-multitenant/middleware/gin/@v/vX.Y.Z.info`

Between step 2 and step 4 the adapter module is only buildable inside this
repository; that window is expected.
