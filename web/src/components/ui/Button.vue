<script setup>
import { cva } from 'class-variance-authority'
import { cn } from '@/lib/utils'

const props = defineProps({
  variant: { type: String, default: 'default' },
  size: { type: String, default: 'default' },
  type: { type: String, default: 'button' },
})

const buttonVariants = cva(
  'inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-md text-[13px] font-medium transition-[color,background-color,border-color,box-shadow] duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40 focus-visible:ring-offset-1 focus-visible:ring-offset-background disabled:pointer-events-none disabled:opacity-50 [&_svg]:size-4 [&_svg]:shrink-0',
  {
    variants: {
      variant: {
        default: 'bg-primary text-primary-foreground shadow-sm hover:bg-primary/90',
        secondary: 'bg-secondary text-secondary-foreground hover:bg-secondary/70',
        outline: 'border border-input bg-background hover:bg-accent hover:text-accent-foreground',
        ghost: 'hover:bg-accent hover:text-accent-foreground',
        destructive: 'bg-destructive text-destructive-foreground shadow-sm hover:bg-destructive/90',
        soft: 'bg-primary/10 text-primary hover:bg-primary/15',
        link: 'text-primary underline-offset-4 hover:underline',
      },
      size: {
        default: 'h-9 px-3.5',
        sm: 'h-8 rounded-md px-2.5 text-xs',
        lg: 'h-10 rounded-md px-5',
        icon: 'size-9',
        'icon-sm': 'size-8 rounded-md',
      },
    },
    defaultVariants: { variant: 'default', size: 'default' },
  },
)
</script>

<template>
  <button :type="type" :class="cn(buttonVariants({ variant, size }))">
    <slot />
  </button>
</template>
