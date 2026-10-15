### `Shop.Billing.charge/2` · cartographer

| | |
|---|---|
| Kind | function |
| Location | [`lib/shop/billing.ex:5`](https://github.com/example/shop-app/blob/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/lib/shop/billing.ex#L5) · shop-app |
| Callers | 2 |
| Callees | 1 |
| Cross-repo callers | 1 |
| Unresolved call sites | 2 (dynamic, imported or outside the map) |

```mermaid
graph LR
  subgraph r0["shop-app"]
    n0["Shop.Billing.charge/2"]
    n1["ShopWeb.OrderController.create/2"]
    n3["Shop.Orders.total/1"]
  end
  subgraph r1["shop-worker"]
    n2["Worker.Nightly.run/0"]
  end
  n0 --> n3
  n1 --> n0
  n2 -.->|"heuristic"| n0
  classDef focus fill:#fff3bf,stroke:#f08c00,stroke-width:2px
  classDef added fill:#d3f9d8,stroke:#2b8a3e
  classDef removed fill:#ffe3e3,stroke:#c92a2a,stroke-dasharray:4 3
  classDef changed fill:#fff3bf,stroke:#e67700
  classDef note fill:#f1f3f5,stroke:#adb5bd,color:#495057
  class n0 focus
```

**Source** (pinned to the snapshot revisions)

- [`Shop.Billing.charge/2`](https://github.com/example/shop-app/blob/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/lib/shop/billing.ex#L5)
- caller [`ShopWeb.OrderController.create/2`](https://github.com/example/shop-app/blob/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/lib/shop_web/order_controller.ex#L8)
- caller [`Worker.Nightly.run/0`](https://github.com/example/shop-worker/blob/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/lib/worker/nightly.ex#L3) ⇄ cross-repo
- callee [`Shop.Orders.total/1`](https://github.com/example/shop-app/blob/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/lib/shop/orders.ex#L20)

<sub>map snapshot-20261015T120000Z · shop-app@aaaaaaa shop-worker@bbbbbbb · <code>cartographer show shop-app:Shop.Billing.charge/2</code></sub>
