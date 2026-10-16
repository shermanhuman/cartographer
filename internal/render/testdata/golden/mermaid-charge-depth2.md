graph LR
  %% map snapshot-20261015T120000Z · shop-app@aaaaaaa shop-worker@bbbbbbb
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
