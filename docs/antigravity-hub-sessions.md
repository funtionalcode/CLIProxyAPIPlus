# Antigravity Hub compatibility

The generation executor separates the upstream installation session from the
downstream conversation. This applies to Gemini and Claude requests, including
streaming and non-streaming execution.

- `request.sessionId` is a stable negative decimal int64 derived from the
  credential identity. Caller-provided session IDs select conversations locally;
  they are not forwarded as installation IDs.
- An explicit session header, body session ID, or execution-session metadata
  selects a conversation scoped by credential, caller and model. The existing
  session extractor defines the supported client headers and body fields.
- Without an explicit session, each request starts an independent trajectory.
  Inferred prompt hashes remain available to the existing routing and reasoning
  replay systems, but are not used to merge upstream Hub conversations.
- Agent request IDs use `agent/<conversation>/<unix-ms>/<trajectory>/<step>`.
  The corresponding labels use the same conversation and trajectory, a stable
  `<trajectory>-0` request label, and a zero-based `last_step_index`. Allocation
  is serialized so simultaneous continuations receive different steps.
- Native image requests receive an independent `image_gen/<unix-ms>/<uuid>/1`
  ID and omit agent session/labels. Independent `web_search` requests retain
  their existing envelope behavior.

The trajectory cache is local to an executor, bounded to 4,096 conversations and
expires after 30 minutes of inactivity. Eviction, restart, or execution on a
different service instance starts a fresh random trajectory. It does not depend
on a shared Home or Redis counter, and does not promise trajectory continuity
across instances. Credential-derived installation IDs remain stable.

All generated fields are applied before the final payload configuration barrier.
Existing payload override/filter rules can therefore replace or remove them.
No automatic account disabling is introduced. Scheduled OAuth refresh and the
request-time safety window both use the shared five-minute refresh lead; the
existing singleflight token-refresh mechanism remains in place.

## Device profiles

Device profiles are enabled by default. A credential identity deterministically
selects a platform/architecture and language. The same account keeps its profile
across requests, token refreshes and restarts; different accounts can use different
profiles. Renaming the identity can change the profile. This is application-level
User-Agent and Accept-Language selection, not TLS or hardware fingerprint spoofing.

Generation, streaming, token counting, credit refresh and model discovery use the
shared profile resolver. Versions continue following the existing Hub updater;
this feature does not assign random old releases. Credential `user_agent` overrides
take precedence. An optional credential `device_profile` object can pin a supported
`os_type`/`arch`, `locale`, `hub_version`, and matching `cl`. Invalid fields are ignored.
Known CL values are paired with their release; unknown versions do not borrow a CL.
Setting `device-profile.enabled: false` restores the existing canonical UA behavior.

## Built-in instructions

An original compatibility template is prepended to the system instruction for
text/code generation, including token counting. It describes the declared client
profile, real tool availability, permission boundaries, and caller output contracts.
Existing caller system parts keep their role and order. Both `systemInstruction`
and its `system_instruction` alias are supported. Image and independent web-search
requests are excluded. This is not a copy of Google's internal system prompt.

Set `system-prompt.enabled: false` to disable it. `system-prompt.text` replaces the
template and supports `{hub_version}`, `{os}`, and `{arch}` placeholders. All prompt
injection runs before final payload rules; those rules can replace or remove it.

## Telemetry preview and opt-in delivery

The default `telemetry.mode: preview` records an INFO log with
`component=antigravity_telemetry`. It makes no telemetry network request. Normal
logger configuration determines whether these previews go to console or a log
file. `off` disables previews and sends; unknown mode strings also disable both.

The allowlisted event contains the actual request model, conversation/trajectory
IDs and step, client version/platform, response ID/model, cumulative token counts,
HTTP status and completion outcome. The local account reference is a one-way hash.
Prompt text, response/thinking text, tool definitions/results, tokens, account
email and raw error bodies are not included. This setting does not change the
separate existing request/response logging feature.

Only explicitly setting `telemetry.mode: send` enables best-effort generation-event
delivery to `https://play.googleapis.com/log`, using the account's OAuth token and
existing proxy transport. The protobuf includes only the observed generation-event
subset. It does not include the local account hash/status or invent bootstrap
events, tool traces, model enum IDs, or timing statistics. Completed successful
responses can be sent; failed, cancelled and incomplete attempts remain local.
Streaming uses the final cumulative counters, not a sum of repeated chunks.

Delivery has four process-wide slots, drops work when busy, does not follow
redirects and does not retry. Delivery failures do not fail model requests or
disable accounts. It adds no network timeout; a stalled send occupies a slot until
the transport returns or the process exits. Changing to `off` affects new model
attempts; an already started send is not recalled. An HTTP acknowledgement only
confirms delivery, not Google's ingestion or account eligibility.

Legacy configuration (as in `config.example.yaml`):

```yaml
antigravity:
  device-profile:
    enabled: true
  system-prompt:
    enabled: true
    text: ""
  telemetry:
    mode: preview
```

For config-version 8, put the same three entries under
`oauth.providers.antigravity`. Existing config hot reload applies them to new
requests. Leave `preview` in place unless external delivery is explicitly desired.

## Reference and validation

Protocol observations were reviewed in
[`Wzh0718/gcl2api-plus`](https://github.com/Wzh0718/gcl2api-plus)
at commit `caabbaa`, especially `src/api/antigravity.py` and
`docs/antigravity-session-and-refresh.md`. The Go implementation is independent;
no source code, bundled prompts, captured telemetry payloads, or weighted profile
tables were imported. The telemetry field numbers follow the documented observed
Hub wire schema. Behavior and upstream acceptance can change independently.

Local tests verify request envelopes, scope isolation, concurrent steps,
expiration/eviction, image handling, payload-rule ordering, profile stability,
prompt preservation, config round trips, telemetry privacy and opt-in delivery.
Telemetry network tests use a fake transport and never contact Google. They do not prove
that an account rejected upstream with `TOS_VIOLATION` can generate responses.
