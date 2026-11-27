import { render } from "@solidjs/web";

import "./ui/styles/index.css";

import { App } from "./app";

render(() => <App />, document.getElementById("root") as HTMLElement);
