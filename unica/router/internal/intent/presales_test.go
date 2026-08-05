package intent

import "testing"

// TestClassify_PreSalesStaysWithAI guards the product's core promise: a pre-sales
// consultation must reach the model, never a human. Every query below is one a
// small merchant's customer types before buying, including the policy questions
// (退/换/保修) that the ontology answers deterministically -- those are still
// consultation, not an account action, and must not be intercepted just because
// they contain a refund-shaped word.
//
// The table deliberately includes queries whose literal characters overlap the
// escalation and handoff vocabularies (a price containing 315, 工商银行 as a
// payment method, 真人试穿 photos, 差评 as a quality question, a store 地址):
// those are the substring collisions that silently cost pre-sales conversions.
func TestClassify_PreSalesStaysWithAI(t *testing.T) {
	cases := []struct {
		category string
		query    string
	}{
		// 商品比较 - product comparison
		{"商品比较", "这两款榨汁机哪个更好用"},
		{"商品比较", "旗舰版和标准版有什么区别"},
		{"商品比较", "3150的和2999的哪个更值得买"},
		{"商品比较", "大号和中号哪个更适合一米七"},
		{"商品比较", "这款和上一代比升级了什么"},

		// 价格/优惠 - price and discounts
		{"价格优惠", "这个多少钱"},
		{"价格优惠", "有优惠券吗"},
		{"价格优惠", "能便宜点吗"},
		{"价格优惠", "买两件有折扣吗"},
		{"价格优惠", "满减和优惠券能一起用吗"},
		{"价格优惠", "支持工商银行信用卡分期吗"},

		// 库存/上新 - stock and restock
		{"库存上新", "现在有货吗"},
		{"库存上新", "什么时候补货"},
		{"库存上新", "新款什么时候上架"},
		{"库存上新", "这个颜色还有库存吗"},

		// 规格参数 - specifications
		{"规格参数", "有黑色的吗"},
		{"规格参数", "尺寸多大"},
		{"规格参数", "这款手机电池多少毫安"},
		{"规格参数", "面料是纯棉的吗"},
		{"规格参数", "有真人试穿的效果图吗"},

		// 物流时效 - shipping and delivery time
		{"物流时效", "发什么快递"},
		{"物流时效", "多久能到"},
		{"物流时效", "包邮吗"},
		{"物流时效", "新疆偏远地区能发货吗"},
		{"物流时效", "今天下单明天能到吗"},

		// 售后政策咨询 - after-sales policy asked BEFORE buying
		{"售后政策", "可以退货吗"},
		{"售后政策", "保修多久"},
		{"售后政策", "怎么退换"},
		{"售后政策", "支持七天无理由退换货吗"},
		{"售后政策", "换货运费谁承担"},
		{"售后政策", "退货是寄回哪里"},
		{"售后政策", "如果要退货运费谁出"},
		{"售后政策", "要退款的话一般多久到账"},

		// 店铺信任 - shop trust questions, still pre-sales
		{"店铺信任", "这款差评多吗，质量到底怎么样"},
		{"店铺信任", "给我发个线下门店的地址"},
	}

	for _, tc := range cases {
		t.Run(tc.category+"/"+tc.query, func(t *testing.T) {
			got := Classify(tc.query)
			if got.NeedsHuman() {
				t.Errorf("pre-sales query intercepted: %q\n  got  class=%s reason=%s matched=%q\n  want class=%s (answered by the AI)",
					tc.query, got.Class, got.Reason, got.Matched, Informational)
			}
		})
	}
}

// TestClassify_HumanNeedingStillIntercepted is the negative control for the test
// above: loosening the word lists to protect pre-sales must not blind the
// classifier to the messages only a human can resolve.
func TestClassify_HumanNeedingStillIntercepted(t *testing.T) {
	cases := []struct {
		category string
		query    string
		want     Class
	}{
		{"账号操作", "我的账号被盗了", Transactional},
		{"账号操作", "我的账号登录不上了，密码也改不了", Transactional},
		{"个人记录操作", "麻烦帮我改一下收货地址", Transactional},
		{"个人记录操作", "我的快递到哪了", Transactional},
		{"个人记录操作", "我要取消订单", Transactional},
		{"投诉升级", "我要投诉你们客服态度太差", Emotional},
		{"投诉升级", "再不解决我就去12315", Emotional},
		{"退款诉求", "已经付款了但一直没收到货，我要退款", Transactional},
		{"退款诉求", "已经付款了没收到货要退款", Transactional},
		{"转人工", "转人工客服", Transactional},
	}

	for _, tc := range cases {
		t.Run(tc.category+"/"+tc.query, func(t *testing.T) {
			got := Classify(tc.query)
			if !got.NeedsHuman() {
				t.Errorf("human-needing query answered by the AI: %q\n  got class=%s reason=%s", tc.query, got.Class, got.Reason)
			}
			if got.Class != tc.want {
				t.Errorf("%q: got class=%s (%s), want %s", tc.query, got.Class, got.Reason, tc.want)
			}
		})
	}
}
