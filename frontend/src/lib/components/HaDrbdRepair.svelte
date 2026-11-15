<script>
  /**
   * HaDrbdRepair — "Storage replica out of sync" card. Shows, per DRBD
   * resource, which node holds the stale copy, the risk, and the repair
   * commands in order with the node each one must run on. Nothing is run
   * from here. ADVANCED also shows the raw issue data.
   */
  import { t } from '$lib/i18n.svelte.js';
  import CopyCommands from '$lib/components/CopyCommands.svelte';

  let { issues = [], advanced = false } = $props();

  const NAME = '[A-Za-z0-9._-]{1,128}';
  const SAFE_CMD = new RegExp(
    `^sudo drbdadm (disconnect|connect|connect --discard-my-data) ${NAME}:${NAME}$`
  );
  const isStale = (d) => d === 'Inconsistent' || d === 'Outdated';

  // Defense in depth: the backend already validated names, but the UI only
  // renders commands of the exact expected shape, and never a discard that
  // targets the local node unless the backend proved it safe.
  function steps(issue) {
    return (issue.repair_steps || []).filter((s) => {
      if (!SAFE_CMD.test(s.command || '')) return false;
      if (s.command.includes('--discard-my-data') && s.node === issue.local_node) {
        return issue.safe_to_discard_local === true;
      }
      return true;
    });
  }

  // Consecutive steps on the same node form one block to copy.
  function groups(issue) {
    const out = [];
    for (const s of steps(issue)) {
      const last = out[out.length - 1];
      if (last && last.node === s.node && last.verify === !!s.verify_peer_stale) {
        last.commands.push(s.command);
      } else {
        out.push({ node: s.node, verify: !!s.verify_peer_stale, commands: [s.command] });
      }
    }
    return out;
  }

  function staleNode(issue) {
    if (issue.kind === 'split_brain_suspected') return '';
    if (isStale(issue.local_disk)) return issue.local_node;
    return issue.peer;
  }
  const goodNode = (issue) => (isStale(issue.local_disk) ? issue.peer : issue.local_node);
  const kindText = (k) => {
    const key = `haDrbd.kind.${k}`;
    const v = t(key);
    return v === key ? k : v;
  };
</script>

{#if issues?.length}
  <section
    class="space-y-3 rounded border border-warning/40 bg-warning/10 p-3"
    aria-labelledby="ha-drbd-title"
    data-testid="ha-drbd-repair"
  >
    <h3 id="ha-drbd-title" class="text-sm font-semibold text-foreground">{t('haDrbd.title')}</h3>
    {#if !advanced}
      <p class="text-xs text-foreground">{t('haDrbd.explain')}</p>
    {/if}
    {#each issues as issue, i (`${issue.resource}-${issue.peer}-${i}`)}
      {@const gs = groups(issue)}
      {@const stale = staleNode(issue)}
      <div
        class="space-y-2 rounded border border-border bg-background p-2"
        data-testid="ha-drbd-issue"
      >
        <p class="text-xs text-foreground">
          {t('haDrbd.resource')}: <code class="break-all">{issue.resource}</code>
          · {kindText(issue.kind)}
        </p>
        {#if stale}
          <p class="text-xs text-foreground" data-testid="ha-drbd-stale">
            {t('haDrbd.staleOn', { node: stale })}
          </p>
          <p class="text-xs text-foreground" data-testid="ha-drbd-risk">
            {t('haDrbd.risk', { stale, good: goodNode(issue) })}
          </p>
        {/if}
        {#if issue.kind === 'split_brain_suspected'}
          <p class="text-xs font-medium text-foreground" data-testid="ha-drbd-manual">
            {t('haDrbd.splitBrain')}
          </p>
        {:else if issue.manual_resolution && !gs.length}
          <p class="text-xs font-medium text-foreground" data-testid="ha-drbd-manual">
            {t('haDrbd.manualOnly')}
          </p>
        {/if}
        {#if isStale(issue.local_disk) && !issue.safe_to_discard_local && issue.kind !== 'split_brain_suspected'}
          <p class="text-xs text-foreground" data-testid="ha-drbd-nodiscard">
            {t('haDrbd.noDiscard', { node: issue.local_node })}
          </p>
        {/if}
        {#each gs as g, n (`${g.node}-${n}`)}
          <div class="space-y-1">
            <p class="text-xs font-semibold text-foreground">
              {t('haDrbd.step', { n: n + 1, node: g.node })}
            </p>
            {#if g.verify}
              <p class="text-xs text-foreground">
                {t('haDrbd.verifyPeer', { node: g.node, resource: issue.resource })}
              </p>
            {/if}
            <CopyCommands commands={g.commands} />
          </div>
        {/each}
        {#if advanced}
          <pre
            class="rounded-md border border-border bg-muted p-2 text-xs font-mono text-foreground whitespace-pre-wrap break-all"
            data-testid="ha-drbd-raw">{JSON.stringify(issue, null, 2)}</pre>
        {/if}
      </div>
    {/each}
  </section>
{/if}
