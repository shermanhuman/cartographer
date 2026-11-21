defmodule AdminWeb do
  def router do
    quote do
      use Phoenix.Router, helpers: false
    end
  end

  defmacro __using__(which) when is_atom(which), do: apply(__MODULE__, which, [])
end
