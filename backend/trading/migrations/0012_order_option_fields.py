from django.db import migrations, models


class Migration(migrations.Migration):

    dependencies = [
        ('trading', '0011_optionposition_nonce_signature'),
    ]

    operations = [
        migrations.AlterField(
            model_name='order',
            name='trade_type',
            field=models.CharField(
                choices=[('STOCK', 'Stock'), ('CFD', 'CFD'), ('OPTION', 'Option')],
                default='STOCK',
                max_length=6,
            ),
        ),
        migrations.AddField(
            model_name='order',
            name='option_contract_type',
            field=models.CharField(blank=True, max_length=4, null=True),
        ),
        migrations.AddField(
            model_name='order',
            name='option_strike',
            field=models.DecimalField(blank=True, decimal_places=4, max_digits=15, null=True),
        ),
        migrations.AddField(
            model_name='order',
            name='option_expiry',
            field=models.DateField(blank=True, null=True),
        ),
    ]
