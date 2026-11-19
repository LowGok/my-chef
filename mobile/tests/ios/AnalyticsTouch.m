#import "AnalyticsTouch.h"
#import <XCTest/XCTest.h>
#import <UIKit/UIKit.h>
#import <TargetConditionals.h>
#include <math.h>

// XCTest has no public multifinger drag API. These test-only selectors match
// Appium WebDriverAgent's native XCTest event declarations at 3d6a4e03cdd673e540289c610f7e189f8ffea434:
// https://github.com/appium/WebDriverAgent/tree/3d6a4e03cdd673e540289c610f7e189f8ffea434/PrivateHeaders/XCTest
// Only declarations are shared; no Appium runtime or downloaded dependency is used.
@interface NSObject (AnalyticsTestTouchSelectors)
- (id)initForTouchAtPoint:(CGPoint)point offset:(double)offset;
- (void)pressDownAtOffset:(double)offset;
- (void)moveToPoint:(CGPoint)point atOffset:(double)offset;
- (void)liftUpAtOffset:(double)offset;
- (id)initWithName:(NSString *)name interfaceOrientation:(long long)orientation;
- (void)addPointerEventPath:(id)path;
- (id)eventSynthesizer;
- (void)synthesizeEvent:(id)record completion:(void (^)(BOOL, NSError *))completion;
@end

static NSInteger AnalyticsVoiceOverGesture(CGRect viewport, BOOL upward, BOOL precise, CGFloat distance, BOOL explore, CGPoint point) {
#if TARGET_OS_SIMULATOR
    if (CGRectIsNull(viewport) || CGRectIsInfinite(viewport)
        || !isfinite(CGRectGetMinX(viewport)) || !isfinite(CGRectGetMinY(viewport))
        || !isfinite(CGRectGetMaxX(viewport)) || !isfinite(CGRectGetMaxY(viewport))
        || viewport.size.width < 200 || viewport.size.height <= viewport.size.width) {
        return 2;
    }
    if (precise && (!isfinite(distance) || fabs(distance) < 2
                    || fabs(distance) > viewport.size.height * 0.3)) return 2;
    if (explore && (!isfinite(point.x) || !isfinite(point.y)
                    || !CGRectContainsPoint(CGRectInset(viewport, 8, 8), point))) return 2;
    @try {
        Class paths = NSClassFromString(@"XCPointerEventPath");
        Class records = NSClassFromString(@"XCSynthesizedEventRecord");
        if (!paths || !records
            || ![paths instancesRespondToSelector:@selector(initForTouchAtPoint:offset:)]
            || ![paths instancesRespondToSelector:@selector(moveToPoint:atOffset:)]
            || ![paths instancesRespondToSelector:@selector(liftUpAtOffset:)]
            || (precise && ![paths instancesRespondToSelector:@selector(pressDownAtOffset:)])
            || ![records instancesRespondToSelector:@selector(initWithName:interfaceOrientation:)]
            || ![records instancesRespondToSelector:@selector(addPointerEventPath:)]
            || ![XCUIDevice.sharedDevice respondsToSelector:@selector(eventSynthesizer)]) {
            return 1;
        }
        id synthesizer = [XCUIDevice.sharedDevice eventSynthesizer];
        if (![synthesizer respondsToSelector:@selector(synthesizeEvent:completion:)]) return 1;
        NSString *name = explore ? @"Analytics VoiceOver passive exploration"
            : precise ? @"Analytics VoiceOver precise scroll" : @"Analytics VoiceOver scroll";
        id record = [[records alloc] initWithName:name interfaceOrientation:UIInterfaceOrientationPortrait];
        if (!record) return 1;
        if (explore) {
            // Request one physical exploration contact. The caller requires
            // VoiceOver and actual passive-focus/UI/speech observations before
            // the separate activation; acknowledgement alone proves no result.
            id path = [[paths alloc] initForTouchAtPoint:point offset:0];
            if (!path) return 1;
            [path liftUpAtOffset:0.06];
            [record addPointerEventPath:path];
        } else if (precise) {
            // VoiceOver's documented standard-gesture entry: double-tap,
            // hold the second contact, then drag while VoiceOver stays on.
            // https://support.apple.com/en-lamr/guide/iphone/iph3e2e2281/27/ios/27
            // The signup content has 24-point horizontal padding. Keep the
            // held contact in its trailing gutter, clear of text and controls.
            CGFloat x = CGRectGetMaxX(viewport) - MAX(12, MIN(16, viewport.size.width * 0.04));
            CGFloat start = CGRectGetMidY(viewport) - distance / 2;
            id drag = [[paths alloc] initForTouchAtPoint:CGPointMake(x, start) offset:0];
            if (!drag) return 1;
            // Preserve the same native pointer for both contacts.
            [drag liftUpAtOffset:0.05];
            [drag pressDownAtOffset:0.15];
            // Hold for 1.25 seconds before dragging; pause before lifting
            // to avoid converting a measured correction into a flick.
            [drag moveToPoint:CGPointMake(x, start) atOffset:1.4];
            // Supply continued movement after the pan threshold, followed by
            // a stationary release. Only measured content motion can verify it.
            for (NSInteger step = 1; step <= 10; step++) {
                CGFloat fraction = (CGFloat)step / 10;
                [drag moveToPoint:CGPointMake(x, start + distance * fraction)
                        atOffset:1.4 + 0.65 * fraction];
            }
            [drag liftUpAtOffset:2.25];
            [record addPointerEventPath:drag];
        } else {
            // Apple VoiceOver scrolls a page with three simultaneous fingers.
            // All paths stay inside the measured app window, away from its edges.
            for (NSInteger finger = 0; finger < 3; finger++) {
                CGFloat x = CGRectGetMidX(viewport) + (finger - 1) * 28;
                CGFloat start = CGRectGetMinY(viewport) + viewport.size.height * (upward ? 0.7 : 0.3);
                CGFloat end = CGRectGetMinY(viewport) + viewport.size.height * (upward ? 0.3 : 0.7);
                id path = [[paths alloc] initForTouchAtPoint:CGPointMake(x, start) offset:0];
                if (!path) return 1;
                [path moveToPoint:CGPointMake(x, end) atOffset:0.45];
                [path liftUpAtOffset:0.5];
                [record addPointerEventPath:path];
            }
        }
        NSLock *lock = [NSLock new];
        __block BOOL finished = NO;
        __block BOOL accepted = NO;
        [synthesizer synthesizeEvent:record completion:^(BOOL result, NSError *error) {
            [lock lock];
            accepted = result && error == nil;
            finished = YES;
            [lock unlock];
        }];
        NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:15];
        while (deadline.timeIntervalSinceNow > 0) {
            [lock lock];
            BOOL done = finished, passed = accepted;
            [lock unlock];
            if (done) return passed ? 0 : 3;
            [NSRunLoop.currentRunLoop runUntilDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
        }
        return 4;
    } @catch (NSException *exception) {
        // No private exception message, arbitrary speech or app data is logged.
        return 5;
    }
#else
    return 1;
#endif
}

NSInteger AnalyticsVoiceOverScroll(CGRect viewport, BOOL upward) {
    return AnalyticsVoiceOverGesture(viewport, upward, NO, 0, NO, CGPointZero);
}

NSInteger AnalyticsVoiceOverFineScroll(CGRect viewport, CGFloat distance) {
    return AnalyticsVoiceOverGesture(viewport, NO, YES, distance, NO, CGPointZero);
}

NSInteger AnalyticsVoiceOverExplore(CGRect viewport, CGPoint point) {
    return AnalyticsVoiceOverGesture(viewport, NO, NO, 0, YES, point);
}
