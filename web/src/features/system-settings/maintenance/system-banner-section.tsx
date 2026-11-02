/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { zodResolver } from '@hookform/resolvers/zod'
import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import type { SystemBannerLevel } from '@/components/system-banner/lib'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'

import {
  SettingsForm,
  SettingsSwitchField,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'

const MAX_TEXT_LENGTH = 500

const bannerText = z
  .string()
  .trim()
  .refine((value) => [...value].length <= MAX_TEXT_LENGTH, {
    message: 'System banner text must be at most 500 characters',
  })

const systemBannerSchema = z
  .object({
    enabled: z.boolean(),
    level: z.enum(['normal', 'important']),
    text_zh: bannerText,
    text_en: bannerText,
  })
  .superRefine((values, ctx) => {
    if (!values.enabled) return
    for (const path of ['text_zh', 'text_en'] as const) {
      if (values[path] === '') {
        ctx.addIssue({
          code: 'custom',
          path: [path],
          message: 'Required while the banner is shown',
        })
      }
    }
  })

type SystemBannerFormValues = z.infer<typeof systemBannerSchema>

type SystemBannerSectionProps = {
  defaultValues: {
    enabled: boolean
    level: SystemBannerLevel
    text_zh: string
    text_en: string
  }
}

const TEXT_FIELDS = [
  { name: 'text_zh', label: 'Chinese text' },
  { name: 'text_en', label: 'English text' },
] as const

export function SystemBannerSection(props: SystemBannerSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const form = useForm<SystemBannerFormValues>({
    resolver: zodResolver(systemBannerSchema),
    defaultValues: props.defaultValues,
  })
  const levelItems = [
    { value: 'normal', label: t('Normal') },
    { value: 'important', label: t('Important') },
  ]

  const { enabled, level, text_zh, text_en } = props.defaultValues
  // Follow saved values only while the form is pristine, so a partly failed
  // save (each success refetches the options) never wipes the admin's edits.
  const isDirty = form.formState.isDirty
  useEffect(() => {
    if (isDirty) return
    form.reset({ enabled, level, text_zh, text_en })
  }, [enabled, level, text_zh, text_en, isDirty, form])

  const onSubmit = async (values: SystemBannerFormValues) => {
    const textChanges = (['level', 'text_zh', 'text_en'] as const)
      .filter((name) => values[name] !== props.defaultValues[name])
      .map((name) => ({ key: `system_banner.${name}`, value: values[name] }))
    const enabledChange =
      values.enabled === props.defaultValues.enabled
        ? []
        : [{ key: 'system_banner.enabled', value: values.enabled }]

    // The backend refuses to enable without both texts and refuses to clear
    // them while enabled, so enabling goes last and disabling goes first.
    const requests = values.enabled
      ? [...textChanges, ...enabledChange]
      : [...enabledChange, ...textChanges]

    for (const request of requests) {
      try {
        await updateOption.mutateAsync(request)
      } catch {
        // useUpdateOption already reported the failure; stop here.
        return
      }
    }
    form.reset(values)
  }

  return (
    <SettingsSection title={t('System banner')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
            saveLabel='Save system banner'
          />
          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchField
                controlId='system-banner-enabled'
                label={t('Show banner')}
                checked={field.value}
                onCheckedChange={field.onChange}
              />
            )}
          />
          <FormField
            control={form.control}
            name='level'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Level')}</FormLabel>
                <Select
                  items={levelItems}
                  value={field.value}
                  onValueChange={(value) => {
                    if (value === 'normal' || value === 'important') {
                      field.onChange(value)
                    }
                  }}
                >
                  <FormControl>
                    <SelectTrigger className='w-48'>
                      <SelectValue />
                    </SelectTrigger>
                  </FormControl>
                  <SelectContent alignItemWithTrigger={false}>
                    <SelectGroup>
                      {levelItems.map((item) => (
                        <SelectItem key={item.value} value={item.value}>
                          {item.label}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <FormMessage />
              </FormItem>
            )}
          />
          {TEXT_FIELDS.map((item) => (
            <FormField
              key={item.name}
              control={form.control}
              name={item.name}
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t(item.label)}</FormLabel>
                  <FormControl>
                    <Textarea rows={3} {...field} />
                  </FormControl>
                  <FormDescription>
                    {[...field.value].length}/{MAX_TEXT_LENGTH}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          ))}
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
