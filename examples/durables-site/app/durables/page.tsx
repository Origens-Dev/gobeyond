import { StartDurables } from "../../components/start-durables.js";
import "../site.css";

export default function DurablesPage() {
  return (
    <main>
      <h1>Durables</h1>
      <p>
        Each button starts a workflow on queue <code>default__local</code>.
        Watch runs in the Temporal UI at{" "}
        <a href="http://localhost:8233">localhost:8233</a>.
      </p>
      <StartDurables />
    </main>
  );
}
