#import <Foundation/Foundation.h>
#import <CoreGraphics/CoreGraphics.h>

// Separate simulator test bundle only. Codes: 0 acknowledged, 1 unavailable,
// 2 invalid geometry, 3 rejected, 4 timeout, 5 Objective-C exception.
NSInteger AnalyticsVoiceOverScroll(CGRect viewport, BOOL upward);

// Standard-gesture correction after an observed page overshoot; <=30% height.
NSInteger AnalyticsVoiceOverFineScroll(CGRect viewport, CGFloat distance);

// One finger, two contacts; only a later app outcome proves activation.
NSInteger AnalyticsVoiceOverActivate(CGRect viewport);
