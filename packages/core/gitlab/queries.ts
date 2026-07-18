export const gitlabKeys = {
  all: ["gitlab"] as const,
  connections: (ws: string) => [...gitlabKeys.all, "connections", ws] as const,
};
