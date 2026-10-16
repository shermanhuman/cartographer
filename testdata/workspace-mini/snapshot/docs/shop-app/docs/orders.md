# Orders

Orders are created through `Shop.Orders.create/2`, which validates input and
inserts into the orders table. Billing (`Shop.Billing`) charges the order
total. See also lib/shop_web/order_controller.ex.
