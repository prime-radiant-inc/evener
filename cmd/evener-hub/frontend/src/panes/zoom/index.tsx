import { lazy } from "react";
import { registerPane } from "../../shell/paneRegistry";
import { parseZoomParams, type SessionZoomParams } from "./intent";

registerPane<SessionZoomParams>({
  id: "sessionZoom",
  title: (params, context) => context.threadName?.(params.ref) ?? params.ref,
  component: lazy(() => import("./Zoom")),
  parseParams: parseZoomParams,
});
