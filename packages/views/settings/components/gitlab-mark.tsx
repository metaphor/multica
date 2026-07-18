// Simplified GitLab fox icon so the GitLab settings tab keeps a
// recognizable icon in the sidebar and section headers.
export function GitLabMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" className={className} fill="currentColor">
      <path d="M12 23.5L15.3 13.5H8.7L12 23.5Z" />
      <path d="M12 23.5L8.7 13.5H2.4L12 23.5Z" />
      <path d="M2.4 13.5L1.1 9.6C1 9.3 1.1 8.9 1.4 8.8C1.7 8.7 2 8.8 2.1 9L4.6 13.5H2.4Z" />
      <path d="M4.6 13.5L8.7 13.5L7 8.5C6.8 7.9 7.6 7.4 8 7.9L4.6 13.5Z" />
      <path d="M12 23.5L15.3 13.5H21.6L12 23.5Z" />
      <path d="M21.6 13.5L22.9 9.6C23 9.3 22.9 8.9 22.6 8.8C22.3 8.7 22 8.8 21.9 9L19.4 13.5H21.6Z" />
      <path d="M19.4 13.5L15.3 13.5L17 8.5C17.2 7.9 16.4 7.4 16 7.9L19.4 13.5Z" />
    </svg>
  );
}
