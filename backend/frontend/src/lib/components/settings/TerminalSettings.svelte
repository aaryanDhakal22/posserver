<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { errText, storeApi, TERMINAL_DEFAULTS, type TerminalSettings } from '$lib/api';
  import { loadPiStatus, type PiLink } from '$lib/store/pi';
  import {
    FieldError,
    FONTS,
    GROUP_KEYS,
    NUM_FIELDS,
    PI_TEXT,
    SAVED_TO,
    settingsForm,
    settingsOf,
    TABS,
    type SettingsForm,
    type TerminalGroup,
    type TextKey
  } from '$lib/store/terminalSettings';
  import { confirmDialog } from '$lib/confirm.svelte';
  import { toast } from '$lib/toast.svelte';
  import Badge from '$lib/ui/Badge.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Switch from '$lib/ui/Switch.svelte';
  import SettingsPage from './SettingsPage.svelte';
  import Section from './Section.svelte';
  import UnitField from './UnitField.svelte';

  // One group of the counter terminals' settings (Store › Settings › Cashier limits, Login and screen, …).
  // PUT /me/terminal/settings with the whole object (main merges over what is saved); every save
  // reaches the Pi as terminal.changed.
  let { group, title, subtitle }: { group: TerminalGroup; title: string; subtitle: string } = $props();

  let saved = $state<TerminalSettings | null>(null);
  let sf = $state<SettingsForm | null>(null);
  let savedForm = $state('');
  let err = $state('');
  let errField = $state<TextKey | ''>('');
  let saving = $state(false);
  let pi = $state<PiLink>('unknown');
  const dirty = $derived(!!sf && JSON.stringify(sf) !== savedForm);
  const D = TERMINAL_DEFAULTS;

  function useSaved(s: TerminalSettings) {
    saved = { ...TERMINAL_DEFAULTS, ...s };
    sf = settingsForm(s);
    savedForm = JSON.stringify(sf);
  }

  onMount(() => {
    storeApi<TerminalSettings>('/me/terminal/settings')
      .then(useSaved)
      .catch((e) => (err = errText(e)));
    loadPiStatus().then((r) => (pi = r.pi));
  });

  async function save() {
    if (!sf || saving) return;
    err = errField = '';
    let body: TerminalSettings;
    try {
      body = settingsOf(sf, saved ?? {});
    } catch (x) {
      err = errText(x);
      if (x instanceof FieldError) {
        errField = x.field;
        await tick();
        document.getElementById('ts-' + x.field)?.focus();
      }
      return;
    }
    saving = true;
    try {
      useSaved(await storeApi<TerminalSettings>('/me/terminal/settings', { method: 'PUT', body: JSON.stringify(body) }));
      toast.success(`${title} saved — ${SAVED_TO[pi]}`);
    } catch (x) {
      err = errText(x);
    } finally {
      saving = false;
    }
  }

  function undo() {
    sf = JSON.parse(savedForm);
    err = errField = '';
  }

  /** Only this page's fields go back to the QuiccPOS defaults (saved on Save). */
  async function fillDefaults() {
    const ok = await confirmDialog({
      title: 'Fill in the defaults?',
      body: `Every setting on this page goes back to the QuiccPOS default. Nothing is saved until you press Save.`,
      confirmLabel: 'Fill in defaults'
    });
    if (!ok || !sf) return;
    const d = settingsForm(TERMINAL_DEFAULTS) as Record<string, unknown>;
    const next = { ...sf } as Record<string, unknown>;
    for (const k of GROUP_KEYS[group]) next[k] = d[k];
    sf = next as SettingsForm;
    err = errField = '';
  }
</script>

{#snippet num(key: TextKey, disabled = false)}
  {@const f = NUM_FIELDS[key]!}
  <UnitField
    id="ts-{key}"
    label={f.label}
    unit={f.unit}
    prefix={f.prefix}
    decimal={f.decimal}
    help={f.help}
    invalid={errField === key}
    {disabled}
    bind:value={() => sf![key], (v) => (sf![key] = v)}
  />
{/snippet}

{#snippet text(key: TextKey, label: string, help: string, rows = 0)}
  <div>
    <label class="lbl" for="ts-{key}">{label}</label>
    {#if rows}
      <textarea id="ts-{key}" class="field" {rows} autocomplete="off" bind:value={() => sf![key], (v) => (sf![key] = v)} aria-invalid={errField === key || undefined}
      ></textarea>
    {:else}
      <input id="ts-{key}" class="field mono" autocomplete="off" bind:value={() => sf![key], (v) => (sf![key] = v)} aria-invalid={errField === key || undefined} />
    {/if}
    <p class="help max-w-[60ch]">{help}</p>
  </div>
{/snippet}

<SettingsPage {title} {subtitle} loading={!sf && !err} {dirty} {saving} error={err} saveLabel="Save" onsave={save} onundo={undo}>
  {#snippet actions()}
    <Button size="sm" variant="ghost" icon="refresh" onclick={fillDefaults} disabled={!sf}>Fill in defaults</Button>
  {/snippet}
  <div class="pi-strip" data-testid="pi-status">
    <Badge tone={PI_TEXT[pi].tone} dot>{PI_TEXT[pi].badge}</Badge>
    <span class="text-text2 text-[13px]">{PI_TEXT[pi].text}</span>
  </div>
  {#if sf}
    {#if group === 'cashier'}
      <Section title="Without a manager’s PIN" description="What a cashier can do on their own. Managers have no limits; a person can also have their own limits (Staff › Edit).">
        <div class="stack">
          {@render num('refund')}
          {@render num('discount')}
          {@render num('priceDrop')}
          {@render num('payout')}
        </div>
      </Section>
    {:else if group === 'screen'}
      <Section title="Logging in" description="How the terminal greets a cashier and when it locks.">
        <div class="stack">
          {@render num('lock')}
          <div>
            <label class="lbl" for="ts-tab">After login, open</label>
            <select id="ts-tab" class="field max-w-[260px]" bind:value={sf.defaultTab}>
              {#each TABS as t (t.value)}<option value={t.value}>{t.label}</option>{/each}
            </select>
            <p class="help">The tab a cashier lands on. Default Info.</p>
          </div>
          {@render num('receiptDone')}
        </div>
      </Section>
      <Section title="The screen" description="How the menu buttons look and sound.">
        <div class="stack">
          <div>
            <label class="lbl" for="ts-font">Menu button font</label>
            <select id="ts-font" class="field max-w-[260px]" bind:value={sf.font}>
              {#each FONTS as f (f.value)}<option value={f.value}>{f.label}</option>{/each}
            </select>
            <p class="help">Default Barlow.</p>
          </div>
          <div class="switches">
            <label class="sw-row"><Switch bind:checked={sf.pricesOnButtons} label="Prices on menu buttons" />Prices on menu buttons</label>
            <label class="sw-row"><Switch bind:checked={sf.clickSound} label="Click sound" />Click sound on buttons</label>
            <label class="sw-row"><Switch bind:checked={sf.showHistory} label="Show the History tab" />Show the History tab</label>
          </div>
        </div>
      </Section>
    {:else if group === 'counter'}
      <Section title="Taking an order" description="What the terminal quotes and allows while a cashier rings an order.">
        <div class="stack">
          {@render num('pickupMinutes')}
          {@render num('toppingMax')}
          {@render num('meal')}
          <label class="sw-row"><Switch bind:checked={sf.nameOnPaid} label="Ask a name for a paid counter order" />Ask a name for a paid counter order</label>
        </div>
      </Section>
      <Section title="Numbers and phone numbers">
        <div class="stack">
          {@render num('counterStart')}
          {@render num('kitchenStart')}
          {@render text('areaCodes', 'Local area codes', `Separated by commas. The terminal uses them to fill in phone numbers. Default ${D.area_codes.join(', ')}.`)}
        </div>
      </Section>
      <Section title="Quibase and deliveries">
        <div class="stack">
          {@render num('quibaseMin')}
          {@render num('margin')}
          <p class="help !mt-0 max-w-[60ch]">A delivery nobody took shows in the drivers’ Dispatch list once it is past the time promised to the customer. The delivery fee (store share + driver share) is in Settings › Fees and tips.</p>
        </div>
      </Section>
    {:else if group === 'voids'}
      <Section title="Reasons a cashier picks" description="Nobody types a reason at the terminal: they pick one of these.">
        <div class="stack">
          {@render text('voidReasons', 'Void reasons', `One per line. For voiding a sent order (the kitchen gets a void ticket); keep an "Other". Default: ${D.void_reasons.join(', ')}.`, 5)}
          {@render text('wasteReasons', 'Waste reasons', `One per line. For voiding an order that was already made (it goes on the report; nothing prints). Default: ${D.waste_reasons.join(', ')}.`, 5)}
        </div>
      </Section>
    {:else if group === 'card'}
      <Section title="Card tips at the counter" description="The card reader can ask the customer for a tip before they pay.">
        <div class="stack">
          <label class="sw-row"><Switch bind:checked={sf.cardTips} label="Ask for a tip on the card reader" />Ask for a tip on the card reader</label>
          <div>
            <label class="lbl" for="ts-tipPercents">Tip choices<span class="sr-only"> (%)</span></label>
            <input
              id="ts-tipPercents"
              class="field mono max-w-[260px]"
              autocomplete="off"
              bind:value={sf.tipPercents}
              disabled={!sf.cardTips}
              aria-invalid={errField === 'tipPercents' || undefined}
            />
            <p class="help">Percentages, separated by commas. Default {D.card_tip_percents.join(', ')}.</p>
          </div>
        </div>
      </Section>
      <Section title="Send to phone" description="A charge the cashier sends to the customer’s phone (Square).">
        {@render num('phoneTimeout')}
      </Section>
    {:else if group === 'endofday'}
      <Section title="Close the batch by itself" description="If nobody pressed Close batch by then, the Pi closes it and prints the driver reports. Default on, at 4:00 am.">
        <div class="stack">
          <label class="sw-row"><Switch bind:checked={sf.autoClose} label="Close the batch by itself" />Close a batch nobody closed</label>
          {#if sf.autoClose}
            <div>
              <label class="lbl" for="ts-autoCloseAt">At (store time)</label>
              <input id="ts-autoCloseAt" class="field mono max-w-[160px]" type="time" bind:value={sf.autoCloseAt} aria-invalid={errField === 'autoCloseAt' || undefined} />
            </div>
          {/if}
        </div>
      </Section>
      <Section
        title="Unpaid counter checks at close"
        description="Closing the day by hand stops while a counter check is unpaid: take the payment or void it. Force close (and the batch closing by itself) then does this. A check with part of it paid is always carried over."
      >
        <div class="choices" role="radiogroup" aria-label="Unpaid counter checks at close">
          <label class="choice">
            <input type="radio" name="ts-closeUnpaid" value="waste" bind:group={sf.closeUnpaid} />
            <span><b>Write them off as waste</b><span class="help !mt-0 block">Each one goes on the report’s Waste section as “Unpaid at close” and a list of them prints. Nothing prints in the kitchen. Default.</span></span>
          </label>
          <label class="choice">
            <input type="radio" name="ts-closeUnpaid" value="carry" bind:group={sf.closeUnpaid} />
            <span><b>Carry them over</b><span class="help !mt-0 block">They stay open like unpaid pickups: payable or voidable the next day, on the Unsettled Check List.</span></span>
          </label>
        </div>
      </Section>
      <Section title="Cash drawers">
        {@render num('drawerStart')}
      </Section>
      <Section
        title="Driver reports"
        description="What each driver hands in at Close batch, on his clock-out slip and on his report: the cash he collected less his card tips after the tip tax."
      >
        <div class="stack">
          <label class="sw-row"><Switch bind:checked={sf.driverPayFromCash} label="Driver delivery pay comes out of the cash at close" />Driver delivery pay comes out of the cash at close</label>
          <p class="help !mt-0 max-w-[60ch]">
            Off: drivers are paid for their deliveries at the end of the week, so the report only counts the deliveries and nothing
            comes off the cash for them. On: the driver’s share of the delivery fee (Fees and tips) for every delivery he made comes
            off the cash he hands in too. Default off.
          </p>
          {@render num('oldDelivery')}
        </div>
      </Section>
    {:else if group === 'receipts'}
      <Section
        title="Customer copies"
        description="The customer’s receipt for a takeout (pickup) order: the one a counter terminal prints when it rings the takeout, and the one the Pi prints for an online, website or call-center pickup."
      >
        <div class="stack">
          <label class="sw-row"><Switch bind:checked={sf.takeoutTwoCopies} label="Takeout: print 2 customer copies" />Takeout: print 2 customer copies</label>
          <p class="help !mt-0 max-w-[60ch]">
            On: every takeout order prints two Customer Copies, each cut, one for the bag. A reprint still prints one. Counter orders and
            deliveries don’t change (a delivery already prints its Merchant Copy and Customer Copy). The Pi’s own receipt copies
            (Quibase › Settings) still count: a pickup the Pi prints takes the larger of the two. Default off.
          </p>
        </div>
      </Section>
    {/if}
  {/if}
</SettingsPage>

<style>
  .pi-strip {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 10px;
  }
  .stack {
    display: flex;
    flex-direction: column;
    gap: 22px;
  }
  .choices {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .choice {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    font-size: 14px;
    cursor: pointer;
  }
  .choice input {
    margin-top: 3px;
    width: 18px;
    height: 18px;
    flex: none;
  }
  .switches {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .sw-row {
    display: inline-flex;
    align-items: center;
    gap: 10px;
    font-size: 14px;
    cursor: pointer;
    width: fit-content;
  }
</style>
