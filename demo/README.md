# Demo lab

A small fake company used to develop, test and demonstrate `wasitus`. Built in
milestone M1 (see ../docs/PLAN.md). Nothing here yet.

Planned contents:

```
demo/
├── docker-compose.yml   # SigNoz + lab services
├── services/
│   ├── storefront/
│   ├── orders/
│   └── wallet/
├── mockpay/             # fake external payment provider, switchable failure modes
├── trafficgen/
└── scenarios/           # S1..S8 stage/reset scripts (see ../docs/SCENARIOS.md)
```
