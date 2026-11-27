import { useNavigate, useParams } from "@solidjs/router";

import { formatCount, joinDot } from "../../lib/format";
import { useTitle } from "../../lib/title";
import { useAppData } from "../../shell/data";
import { EmptyState, SideSheet, Topbar } from "../../ui";

/** The queue: /queue, and /queue/:jobId with that job open in the side sheet. Placeholder. */
export default function QueuePage() {
  const params = useParams();
  const navigate = useNavigate();
  const { status } = useAppData();
  useTitle(() => (params.jobId ? `Job ${params.jobId}` : "Queue"));

  const running = () => status().running.length;
  const queued = () => (status().queue.pending ?? 0) + (status().queue.leased ?? 0) + (status().queue.retrying ?? 0);

  return (
    <>
      <Topbar title="Queue" meta={joinDot(`${formatCount(running())} running`, `${formatCount(queued())} queued`)} />
      <div class="av-workspace">
        <EmptyState icon="list-video" title="This page is not built yet" description="Running and queued jobs go here." />
        <SideSheet open={!!params.jobId} title={`Job ${params.jobId}`} icon="list-video" onClose={() => navigate("/queue")}>
          <EmptyState title="This page is not built yet" description="Attempts, events and the flow path of this job go here." />
        </SideSheet>
      </div>
    </>
  );
}
