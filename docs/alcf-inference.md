# Using the ALCF Inference Endpoints

`alcfauth login` signs in to the inference service and writes a bearer key
that both `lmc` and `apiproxy` can read. `alcfauth status` displays the
cache and key paths, expiry, and login age. Add `-check` to query the gateway.
[Login and first request](../README.md#alcf-inference-endpoints)

## Choose the cluster and API

Use the cluster's model catalog to find a model ID and inspect its advertised
inputs, reasoning effort levels, and APIs. Model discovery uses a separate
URL from the inference API. For Minerva, set the two paths as follows.

```sh
cluster=https://inference-api.alcf.anl.gov/resource_server/minerva
key="$HOME/.lmc/alcf/inference.key"
lmc -provider openai -api-key-file "$key" \
  -models-url "$cluster/models" -list-models
```

For other clusters, get the base path from the service guide. For example,
Minerva uses `/resource_server/minerva/api/v1` for inference, while Sophia
uses `/resource_server/sophia/vllm/v1`.
[ALCF cluster endpoints](https://docs.alcf.anl.gov/services/inference-endpoints/#available-clusters)

To use the same cluster from an API client, choose one of these proxy
commands. In the client, use a model ID from the catalog.

```sh
# OpenAI Chat Completions or Responses client
apiproxy -provider openai -api-key-file "$key" \
  -provider-url "$cluster/api/v1" -models-url "$cluster/models"
```

```sh
# Anthropic Messages client
apiproxy -provider anthropic -anthropic-bearer -api-key-file "$key" \
  -provider-url "$cluster/api/v1" -models-url "$cluster/models"
```

`-anthropic-bearer` sends the gateway's bearer token alongside the
Anthropic key header. If the gateway returns 404 for `count_tokens`, the
proxy supplies a local estimate. On the OpenAI route the proxy forwards
Responses requests to the gateway. If you need response retrieval or
`previous_response_id` on a backend that does not store responses, add
`-chat-completions` to convert those requests and keep the state in the
proxy's sessions directory. [Responses compatibility](../README.md#responses-api-compatibility)

## Keep the key current

Run `alcfauth token -quiet` to ensure the key file contains a valid token
without printing it. With `-watch`, the command stays in the foreground,
refreshes before expiry, and reports progress on stderr. The default refresh
margin is one hour. For a custom margin, use `alcfauth token -watch -min-valid 2h`.
The watch retries transient refresh failures and exits when a new login is
required. Run `alcfauth login` and start it again.

`-cache FILE` selects the token cache and `-key-file FILE` selects the file
holding just the bearer token. Both default to paths under `~/.lmc/alcf/`.
Set the same paths for login, refresh, status, and logout if you override
them. The cache can also use the `tokens-bash.json` layout of ALCF's shell
helper. Use `-api-key-file` with the bearer key. A DEBUG request log or a
printed `curl` command can contain the token.

### Linux user service

After logging in, save this as `~/.config/systemd/user/alcfauth.service`.
Adjust `ExecStart` to the location of your executable. `%h` expands to the
home directory of the user running the service.

```ini
[Unit]
Description=Refresh the ALCF inference key

[Service]
ExecStart=%h/bin/alcfauth token -watch

[Install]
WantedBy=default.target
```

```sh
systemctl --user daemon-reload
systemctl --user enable --now alcfauth.service
systemctl --user status alcfauth.service
```

After a new Globus login, use `systemctl --user restart alcfauth.service`
to resume refreshing.

### macOS launch agent

After logging in, save this as
`~/Library/LaunchAgents/gov.anl.alcf.alcfauth.plist`. Replace both
`/Users/you` paths with absolute paths on your machine. `launchd` passes
the argument strings directly to the executable.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>gov.anl.alcf.alcfauth</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/you/bin/alcfauth</string>
    <string>token</string>
    <string>-watch</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>StandardErrorPath</key><string>/Users/you/.lmc/alcf/watch.log</string>
</dict>
</plist>
```

```sh
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/gov.anl.alcf.alcfauth.plist"
launchctl print "gui/$(id -u)/gov.anl.alcf.alcfauth"
```

After renewing a login, restart the loaded job with
`launchctl kickstart -k "gui/$(id -u)/gov.anl.alcf.alcfauth"`.
