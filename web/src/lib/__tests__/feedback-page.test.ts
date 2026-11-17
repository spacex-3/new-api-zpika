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
import { describe, expect, test } from 'vitest'

import { canAccessFeedbackPage } from '@/lib/feedback-page'
import { ROLE } from '@/lib/roles'

describe('feedback page access', () => {
  test('always permits administrators', () => {
    expect(canAccessFeedbackPage(undefined, ROLE.ADMIN)).toBe(true)
    expect(
      canAccessFeedbackPage(
        { feedback_page_user_visible: false },
        ROLE.SUPER_ADMIN
      )
    ).toBe(true)
  })

  test('permits users only when the public visibility flag is enabled', () => {
    expect(
      canAccessFeedbackPage({ feedback_page_user_visible: true }, ROLE.USER)
    ).toBe(true)
    expect(
      canAccessFeedbackPage({ feedback_page_user_visible: false }, ROLE.USER)
    ).toBe(false)
    expect(canAccessFeedbackPage(undefined, ROLE.USER)).toBe(false)
  })
})
