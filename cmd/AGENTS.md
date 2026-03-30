# Command Package Guidelines

This directory owns Colin's Cobra command tree. Keep command wiring here and keep `../main.go` limited to invoking the exported entrypoint.

## Rules

- Define one constructor per command or subcommand that returns `*cobra.Command`.
- Prefer persistent or local flags for configuration inputs such as workflow paths; do not add positional arguments for config files unless there is a strong CLI reason.
- Route all user-visible command output through Cobra helpers such as `Print`, `Println`, `Printf`, `PrintErr`, `PrintErrln`, and `PrintErrf`.
- When a structured encoder must write directly, target `cmd.OutOrStdout()` or `cmd.ErrOrStderr()` instead of `os.Stdout` or `os.Stderr`.
- Keep `RunE` code paths testable by factoring service work into helpers that accept `*cobra.Command` plus typed options.
- Preserve the current error-handling contract: silence Cobra's default usage/errors, wrap usage failures explicitly, and return exit codes `0`, `1`, and `2` consistently.
- Configure output streams and flag error handling for each command constructor so tests can redirect I/O cleanly.
