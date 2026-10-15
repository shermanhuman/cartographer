import {Socket} from "phoenix";

export function render(el, opts = {}) {
  return items(el).map(v => format(v, opts));
}

function items(el) {
  return [...el.children];
}

const format = (value, {prefix} = {}) => `${prefix ?? ''}${value}`;

export const Hooks = {
  Chart: {
    mounted() {
      this.handleEvent("points", (data) => render(this.el, data));
    },
  },
};

class Widget {
  update(first, ...rest) {
    console.log(first, rest.length);
  }
}

window.addEventListener("load", () => new Widget().update(1, 2));
