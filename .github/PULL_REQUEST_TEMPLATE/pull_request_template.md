# Pull Request Template

## Description

Brief description of changes.

## Type of Change

- [ ] Bug fix (non-breaking change fixing an issue)
- [ ] New feature (non-breaking change adding functionality)
- [ ] Breaking change (fix or feature that would cause existing functionality to not work as expected)
- [ ] Documentation update
- [ ] Refactoring (no functional changes)
- [ ] Performance improvement
- [ ] Test coverage

## Related Issues

Closes #(issue number)

## Philosophy Check

- [ ] Improves human understanding
- [ ] Reduces noise / complexity
- [ ] Preserves human control
- [ ] Increases trust through visibility
- [ ] Fits local-first
- [ ] Mutations are reversible
- [ ] Graph-first / structure before intelligence
- [ ] Behavior is deterministic

## Testing

- [ ] All existing tests pass (`go test ./...`)
- [ ] New tests added for new functionality
- [ ] Race detector passes (`go test -race ./...`)
- [ ] Linter passes (`go vet ./...` and `staticcheck ./...`)
- [ ] Lynx tests pass (`cd lynx && cargo test`)

## Checklist

- [ ] Code follows project style (gofmt, clippy)
- [ ] Public APIs have Go doc comments
- [ ] Commit messages follow conventional commits
- [ ] Documentation updated if needed
- [ ] No new warnings from linters
- [ ] Config changes documented in README/TECHSTACK

## Screenshots (if UI changes)

<!-- Add screenshots here -->

## Additional Notes

<!-- Any other context -->