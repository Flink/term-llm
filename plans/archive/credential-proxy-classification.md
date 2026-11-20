# Classification providers in the credential proxy

Status: implemented

The presets, exact path matching and opt-in bindings are implemented. Discovery
negotiates the Classification family using the `X-Term-LLM-Proxy-Capabilities`
header on v2. All three backends were verified with local fixture upstreams;
the production canary is an operator step and has not been run.

## Diagnosis

Jev is a model family, not a provider. `jev-latest` is the default model for TypeSafe System One (`typesafe`). The other built-in classification providers are OpenAI Decisions (`openai`) and Cloudflare Workers AI Clef (`cloudflare`). They are resolved under `classify.providers`, separately from chat inference.

The credential proxy's Add provider catalog is projected from `proxyprofile.All()` (`internal/app/proxy/presets.go`). It has no classification profiles. The admin picker also has no Classification family. Separately, managed discovery in `internal/config/proxy_routes.go` does not bind classifiers: its feature binder walks struct fields, but `Classify.Providers` is a map. A picker-only change would not route `term-llm classify`, Guardian, live voice classification, or grep reranking through the proxy. This is a gap in the checked-in integration, not evidence of missing Jev models or a failed vendor login. No live deployment was inspected.

## Unblock TypeSafe now

Create a **Custom endpoint** backed by `https://api.typesafe.ai` with bearer client authentication and upstream `Authorization: Bearer` substituted from a proxy-stored TypeSafe key. Allow `POST =/v1/systemone` and `GET =/v1/models` and no unrelated endpoints. The `=` makes the path match exact; older proxies only support prefix matches without `=`. Approve only the intended clients. On the client, configure a classification alias with the route URL (no appended `/v1`) and its scoped client token, not the vendor key:

```yaml
classify:
  providers:
    typesafe-proxy:
      type: typesafe
      model: jev-latest
      base_url: https://proxy.example.test/typesafe-proxy/
      api_key: file:///run/secrets/classify-proxy-token
```

Use `term-llm classify models -p typesafe-proxy` and `term-llm classify -p typesafe-proxy 'The test light is green.' --type noul --question 'Is the light green?'` to smoke-test the route. Select the alias for Guardian, live voice, or grep separately if desired; do not change their defaults just to test the setup. This uses existing routing and is independent of managed provider discovery.

## Implementation

1. **Add scoped presets.** Add a Classification family to the proxy's profile catalog, server validation and admin UI. Label the TypeSafe preset **TypeSafe (Jev)** so searching for Jev finds it. Reuse the existing saved-secret and client-approval flow. Keep OpenAI Decisions a separate provider/grant from OpenAI chat and ChatGPT plans; it can reference the same stored API key if the operator chooses. Do not widen existing grants, rewrite custom routes, or enable request-body capture or speculative billing as part of this change.

   | Proxy provider ID | Classify selector | Upstream base | Methods and paths relative to that base |
   | --- | --- | --- | --- |
   | `typesafe` | `typesafe` | `https://api.typesafe.ai` | `POST /v1/systemone`, `GET /v1/models` |
   | `openai-decisions` | `openai` | `https://api.openai.com/v1` | `POST /decisions` |
   | `cloudflare-clef` | `cloudflare` | Operator-provided account Workers AI run root or compatible AI Gateway root | `POST /@cf/cloudflare/clef`, `POST /@cf/cloudflare/clef-flash` |

   Use exact method/path matching for the preset defaults without changing existing prefix-rule behavior. Cloudflare account/gateway details are configured on the proxy. TypeSafe's image opt-in header must pass through; OpenAI organization/project headers, if needed, should be set on the route. OpenAI Decisions and Clef list models locally, so their model-list commands do not test forwarding.

2. **Bind managed classifiers.** Give profiles explicit classifier selector metadata and use a typed binder for `Classify.Providers`, not the struct-field binder. Make the first managed release opt-in through explicit proxy provider selection; leave existing proxy-all classifier traffic and `classify.default_provider` unchanged. Document the difference between proxy IDs (`openai-decisions`) and classifier selectors (`openai`); named classifier aliases can keep using manual endpoint/token config initially. Bind the approved route URL and scoped token in memory, retaining local model, timeout and image-format settings. When a selected route is absent/revoked, fail closed rather than evaluating local vendor key or header references or going direct. Managed bindings must reject endpoint overrides (including CLI `--base-url`) that could send the scoped token elsewhere. Reuse shared classification connection resolution so all classifier consumers get the same behavior when they select a bound classifier. Never persist a materialized token.

3. **Keep discovery compatible and roll out deliberately.** Today's v2 client rejects unknown provider families before filtering unknown profiles (`internal/config/proxy_discovery.go`). Have capable clients opt in to seeing the Classification family in discovery; keep legacy discovery responses unchanged, even for clients granted both chat and classifiers. Publish setup and migration instructions in the credential-proxy and classify guides. Canary TypeSafe first with synthetic input, verify a request on the intended route and that revocation blocks the next call, then enable Decisions and Clef for willing clients. Leave existing manual routes and grants untouched unless operators explicitly migrate them.

## Verification and acceptance

- Add Go fixture coverage for preset projection, provider creation, discovery compatibility, path/method enforcement, and scoped grants. Have each backend classify through a real proxy backed by local HTTP fixtures. Check that the upstream receives only its vendor credentials and the expected path; cover TypeSafe model listing and image header forwarding, and both Clef paths.
- Test managed binding of each built-in selector; verify that defaults and unselected/direct/manual aliases are unchanged. Disabled, missing and revoked routes must not contact the vendor or resolve local secrets. Test override rejection, no token persistence, and mixed-version discovery for clients that also have chat grants.
- Add a frontend regression test for the Classification group and a picker search for “Jev.” Run focused proxy/config/classify tests, frontend checks, and `make check-changed` before shipping. Treat a real test classification (not a local model list) as the Decisions and Clef smoke check.
