# Prior art

Quick scan of GitHub on 26 Nov 2026. Not yet reviewed in depth: skim each before
M2 and note anything worth borrowing.

## Open source

| Project | Stars (26 Nov 2026) | What it does | Difference from wasitus |
|---|---|---|---|
| smartsmiledev/PostmortemForge | 26 | Rebuilds a timeline from **exported** evidence, drafts a cited postmortem | Works on exports after the fact; wasitus queries live sources and gives an our-side/external verdict |
| phlx0/postmortem | 3 | Incident timeline from Git + logs | No external-cause classification |
| priyanshu015211/LLM-Powered-Log-Triage | 6 | LLM log triage, grouping, RCA | LLM decides; wasitus uses deterministic, testable rules |
| dreameraiquest/IncidentIQ | 3 | Multi-agent LLM incident analysis from logs | Same as above |
| chatwoot/faultline | 31 | Open-source AI agent for infrastructure debugging | Agent-driven investigation, not a reproducible verdict |

## Commercial

incident.io, Rootly, FireHydrant and PagerDuty build incident timelines, mostly from
chat activity and manual entries, inside a hosted product.

## Where wasitus fits

1. **Our side vs external** is the core output, not a side feature.
2. **Deterministic and testable**: every verdict is backed by rules and evidence,
   checked against a public scenario suite.
3. **OpenTelemetry/SigNoz-native**: uses trace data (outbound calls, server
   address) to locate failures, which log-only tools cannot do as reliably.
4. **Local and read-only**: suitable for fintechs and others that cannot send
   telemetry to a third party.
