defmodule Shop.Billing do
  def charge(order, opts), do: {order, opts}

  def refund(id) do
    # remote call into a dependency
    Stripe.Refund.create(id)
  end
end
