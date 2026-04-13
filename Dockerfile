# Minimal runtime image for skep.
#
# This image ships only the skep binary on top of a distroless base.
# It does NOT include the tools skep shells out to for task execution:
# tmux, git, or Claude Code. Use this image for:
#
#   - Running the stdio MCP server     (skep mcp serve)
#   - Searching the code index         (skep index ask, skep index create)
#   - Reading task state from outside a working tree
#
# For the full skep experience — task execution, the tmux cockpit, the
# approval watchdog, cross-repo delegation that spawns Claude panes —
# install skep on your host from https://skep.sh/install.sh instead.
# A tmux-and-claude-bundled image is planned for v0.2.0.
#
# GoReleaser builds the binary on the host and COPYs it into this image.
FROM gcr.io/distroless/base-debian12:nonroot

LABEL org.opencontainers.image.source="https://github.com/ChaitanyaPinapaka/skep"
LABEL org.opencontainers.image.description="Skep — persistent per-repo code index and MCP server for Claude Code. Index-only container; task execution requires the host install."
LABEL org.opencontainers.image.licenses="MIT"

COPY skep /usr/local/bin/skep

USER nonroot:nonroot
WORKDIR /workspace

# Declare /workspace as a volume so `docker run skep ...` without an
# explicit -v mount produces an anonymous volume and a visible warning
# instead of silently writing to a transient layer that vanishes at
# exit. Users mount their repo at /workspace.
VOLUME ["/workspace"]

ENTRYPOINT ["/usr/local/bin/skep"]
