defmodule Shop.OrdersTest do
  use ExUnit.Case
  alias Shop.Orders

  test "total of nothing" do
    assert Orders.total([]) == 0
  end

  defp sample, do: Orders.create(%{})
end
