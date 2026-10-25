# Nightshift

An autonomous ticket-execution harness. Run it inside a git repository in order
to find open GitHub issues. After confirmation, the harness drives agents to
implement each issue in a separate git worktree and open a pull request.

Nightshift treats the **current working directory as the project**, so it works on
any repo without additional configuration.
