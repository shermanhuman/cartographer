graph LR
  %% map snapshot-20261015T120000Z · shop-app@aaaaaaa shop-worker@bbbbbbb
  n0["Shop.Orders.create/2"]
  n1["Shop.Orders.total/1"]
  n2["Shop.Orders.validate/1"]
  n0 -->|"×2"| n2
  more["+3 more → cartographer impact shop-app:Shop.Orders"]
  classDef focus fill:#fff3bf,stroke:#f08c00,stroke-width:2px
  classDef added fill:#d3f9d8,stroke:#2b8a3e
  classDef removed fill:#ffe3e3,stroke:#c92a2a,stroke-dasharray:4 3
  classDef changed fill:#fff3bf,stroke:#e67700
  classDef note fill:#f1f3f5,stroke:#adb5bd,color:#495057
  class n0,n1,n2 focus
  class more note
