<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLButtonAttributes } from 'svelte/elements';
  import Icon, { type IconName } from './Icon.svelte';

  type Variant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'danger-ghost';
  type Size = 'xs' | 'sm' | 'md' | 'lg';

  let {
    variant = 'secondary',
    size = 'md',
    href,
    download,
    icon,
    iconRight,
    loading = false,
    disabled = false,
    type = 'button',
    class: cls = '',
    children,
    ...rest
  }: Omit<HTMLButtonAttributes, 'type'> & {
    variant?: Variant;
    size?: Size;
    href?: string;
    download?: string | boolean;
    icon?: IconName;
    iconRight?: IconName;
    loading?: boolean;
    type?: 'button' | 'submit' | 'reset';
    class?: string;
    children?: Snippet;
  } = $props();

  const V: Record<Variant, string> = {
    primary: 'btn-p',
    secondary: '',
    ghost: 'btn-g',
    danger: 'btn-danger',
    'danger-ghost': 'btn-danger-g'
  };
  const S: Record<Size, string> = { xs: 'btn-xs', sm: 'btn-sm', md: '', lg: 'btn-lg' };
  const iconOnly = $derived(!children && !!icon);
  const classes = $derived(`btn ${V[variant]} ${S[size]} ${iconOnly ? 'btn-icon' : ''} ${cls}`);
  const iconSize = $derived(size === 'xs' ? 14 : 16);
</script>

{#if href}
  <a {href} {download} class={classes} aria-disabled={disabled || undefined} {...rest as Record<string, unknown>}>
    {#if icon}<Icon name={icon} size={iconSize} stroke={variant === 'primary' ? 2.4 : 2} />{/if}
    {@render children?.()}
    {#if iconRight}<Icon name={iconRight} size={14} />{/if}
  </a>
{:else}
  <button {type} class={classes} disabled={disabled || loading} aria-busy={loading || undefined} {...rest}>
    {#if loading}
      <span class="h-3.5 w-3.5 rounded-full border-2 border-current border-t-transparent animate-spin" aria-hidden="true"></span>
    {:else if icon}
      <Icon name={icon} size={iconSize} stroke={variant === 'primary' ? 2.4 : 2} />
    {/if}
    {@render children?.()}
    {#if iconRight}<Icon name={iconRight} size={14} />{/if}
  </button>
{/if}
