### `public.orders` · cartographer

| | |
|---|---|
| Kind | table |
| Functions | 1 |
| Callers | 1 |
| Callees | 1 |
| Cross-repo callers | 0 |

```mermaid
graph LR
  n0["Shop.Orders.create/2"]
  n1["ShopWeb.OrderController.create/2"]
  n2["Shop.Orders.validate/1"]
  n0 -->|"×2"| n2
  n1 --> n0
  classDef focus fill:#fff3bf,stroke:#f08c00,stroke-width:2px
  classDef added fill:#d3f9d8,stroke:#2b8a3e
  classDef removed fill:#ffe3e3,stroke:#c92a2a,stroke-dasharray:4 3
  classDef changed fill:#fff3bf,stroke:#e67700
  classDef note fill:#f1f3f5,stroke:#adb5bd,color:#495057
  class n0 focus
```

**Source** (pinned to the snapshot revisions)

- [`Shop.Orders.create/2`](https://github.com/example/shop-app/blob/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/lib/shop/orders.ex#L10)
- caller [`ShopWeb.OrderController.create/2`](https://github.com/example/shop-app/blob/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/lib/shop_web/order_controller.ex#L8)
- callee [`Shop.Orders.validate/1`](https://github.com/example/shop-app/blob/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/lib/shop/orders.ex#L30)

<sub>map snapshot-20261015T120000Z · shop-app@aaaaaaa shop-worker@bbbbbbb · <code>cartographer show public.orders</code></sub>
