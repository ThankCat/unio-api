# Observability runtime configuration

This directory contains the Alloy and Loki runtime configuration shared by the
Dev and Test Compose environments. Environment-specific log sources remain in
their Compose files; both mount the Gateway JSONL log at
`/var/log/unio/gateway.jsonl` inside Alloy.

Loki runs with `auth_enabled: false`. In this single-tenant mode Loki uses
`fake` as its built-in tenant ID, so local ruler files must remain under
`loki/rules/fake/`. The name does not mean that the alert rules are test data.
