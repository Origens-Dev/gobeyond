import { StartDurables } from "../../components/start-durables.js";
import "../site.css";

export default function DurablesPage() {
  return (
    <main>
      <h1>Durables</h1>
      <p>
        Hit a button to kick off a workflow. Locally you can use Temporal to
        exercise these actions — for quick UI testing, open the Temporal UI at{" "}
        <a href="http://localhost:8233">localhost:8233</a> and watch the run
        show up. Queue is <code>default__local</code>.
      </p>
      <StartDurables />
    </main>
  );
}
