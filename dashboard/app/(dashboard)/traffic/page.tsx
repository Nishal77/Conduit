import { TrafficFeed } from "@/components/traffic/traffic-feed";

export default function TrafficPage() {
  return (
    <div className="flex h-full flex-col gap-4">
      <div>
        <h1 className="text-xl font-semibold">Live Traffic</h1>
        <p className="text-sm text-muted-foreground">Real-time feed of tool calls as Conduit processes them.</p>
      </div>
      <div className="min-h-0 flex-1">
        <TrafficFeed />
      </div>
    </div>
  );
}
