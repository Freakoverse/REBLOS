#!/bin/sh
set -e

# /app/data is often a bind-mounted host directory that Docker creates owned by
# root, which the unprivileged app user can't write to. Fix ownership when needed
# (only when the app user can't already write — so we don't do a slow recursive
# chown of a large disk-backend blob tree on every start), then drop privileges.
if ! su-exec degnode test -w /app/data 2>/dev/null; then
	chown -R degnode:degnode /app/data
fi

exec su-exec degnode "$@"
