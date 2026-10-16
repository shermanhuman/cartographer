defmodule Shop.Orders do
  @moduledoc "Orders context."
  alias Shop.Billing
  alias Shop.Orders.Order, as: O
  import Shop.Format, only: [cents: 1]

  def create(attrs, opts \\ []) do
    attrs
    |> validate()
    |> Billing.charge(opts)
  end

  def total([]), do: 0
  def total([item | rest]), do: item.price + total(rest)

  def label(order), do: cents(order.total) <> " " <> to_string(order.id)

  def new, do: %O{}

  def refund_all(ids), do: Enum.map(ids, &Billing.refund/1)

  def run_later(mod, fun), do: apply(mod, fun, [])

  def run_known, do: apply(Billing, :refund, [1])

  defdelegate refund(id), to: Billing

  defp validate(attrs), do: attrs
end
