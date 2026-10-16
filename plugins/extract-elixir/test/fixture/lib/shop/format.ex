defmodule Shop.Format do
  def cents(n), do: "#{n}c"

  for unit <- [:kg, :lb] do
    def unquote(unit)(n), do: {unquote(unit), n}
  end
end
