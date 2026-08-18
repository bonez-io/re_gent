# re_gent UI epic

The first re_gent UI is a read-only history explorer for the same project data
available through the server. It answers three questions:

1. What have agents and people been doing in this project?
2. Why did a particular step happen?
3. Which step produced this file or line?

This directory is the design source of truth for epic #36:

- [architecture.md](architecture.md) locks the frontend stack, repository
  boundary, deployment modes, and security boundary.
- [wireframes.md](wireframes.md) defines the information architecture,
  desktop/mobile layouts, routes, and required UI states.

The UI is intentionally read-only in v1. Authentication administration,
billing, destructive history operations, rewind, and merge controls are not
part of this epic.
