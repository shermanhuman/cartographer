defmodule Shop.Orders.Order do
  use Ecto.Schema
  import Ecto.Query

  schema "orders" do
    field :total, :integer
    belongs_to :customer, Shop.Customer
  end

  def recent_query do
    # orders placed this week
    from o in "orders", where: o.inserted_at > ago(7, "day")
  end
end
